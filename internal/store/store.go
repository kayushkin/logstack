package store

import (
	"bufio"
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kayushkin/logstack/models"
)

// Store defines the interface for log storage
type Store interface {
	// Write adds a new log entry
	Write(entry *models.LogEntry) error

	// Query searches for logs matching the given parameters
	Query(params models.QueryParams) ([]models.LogEntry, error)

	// Group aggregates logs by a specific field
	Group(params models.QueryParams, groupBy string) ([]models.GroupedLogs, error)

	// Stats returns aggregate statistics
	Stats(params models.QueryParams) (*models.Stats, error)

	// Usage returns aggregated token usage grouped by agent+orchestrator
	Usage(from time.Time) ([]models.UsageStats, error)

	// MaxUsage returns comprehensive Max subscription usage for a billing period
	MaxUsage(from, to time.Time) (*models.MaxUsageResponse, error)

	// RateLimits returns recent 429 error events
	RateLimits(from time.Time, limit int) (*models.RateLimitsResponse, error)

	// Get retrieves a single log by ID
	Get(id string) (*models.LogEntry, error)

	// Delete removes logs matching params
	Delete(params models.QueryParams) (int, error)
}

// FileStore implements Store using JSONL files
type FileStore struct {
	baseDir string

	// mu guards index, and nothing else. The scan paths (Query, Usage,
	// MaxUsage, RateLimits) read only baseDir — which is immutable after
	// construction — and the filesystem, so they must NOT take it.
	//
	// This is load-bearing, not a style note. Query used to hold mu.RLock
	// for the whole of an unscoped 30-day, ~90MB disk scan. An ingest Write
	// would then block on Lock(), and because Go's RWMutex stops admitting
	// new readers once a writer is waiting, every subsequent read and write
	// parked behind it: one anonymous GET /logs/group/<field> stalled the
	// entire log sink for ~2 minutes while /health kept cheerfully
	// answering 200. See TestQueryDoesNotTakeTheIndexLock.
	mu sync.RWMutex

	// Index for faster lookups (simple in-memory index)
	index map[string]string // id -> file path
}

// NewFileStore creates a new file-based store
func NewFileStore(baseDir string) (*FileStore, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}

	store := &FileStore{
		baseDir: baseDir,
		index:   make(map[string]string),
	}

	// Build initial index from existing files
	if err := store.buildIndex(); err != nil {
		return nil, fmt.Errorf("build index: %w", err)
	}

	return store, nil
}

// Write adds a new log entry to the store
func (s *FileStore) Write(entry *models.LogEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Organize by date and orchestrator
	// Structure: baseDir/YYYY-MM-DD/orchestrator.jsonl
	dateStr := entry.Timestamp.Format("2006-01-02")
	filename := fmt.Sprintf("%s.jsonl", entry.Orchestrator)
	if entry.Orchestrator == "" {
		filename = "unknown.jsonl"
	}

	dir := filepath.Join(s.baseDir, dateStr)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create date dir: %w", err)
	}

	path := filepath.Join(dir, filename)

	// Append to file
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write entry: %w", err)
	}

	// Update index
	s.index[entry.ID] = path

	return nil
}

// Query searches for logs matching the given parameters.
//
// Deliberately takes no lock: it reads the filesystem and the immutable
// baseDir, never index. Holding mu here starved ingest — see the mu comment.
//
// A bounded query never materialises the corpus. Query used to parse every
// entry in the window into one slice, sort all of them, and only then slice off
// Offset/Limit — so the default `GET /logs` (Limit=100, no window) parsed
// ~192k entries, allocated ~300MB, and threw 192,090 of them away to return a
// hundred. The work a request does was set by how much history happened to be on
// disk rather than by what the request asked for. selectEntries keeps only the
// Offset+Limit entries that can still survive that slicing, so peak memory is
// now set by the caller's own bound.
//
// The unbounded path (Limit == 0, which is what Group and Stats use) is
// unchanged and still holds everything: it has to, because it returns
// everything.
func (s *FileStore) Query(params models.QueryParams) ([]models.LogEntry, error) {
	// Only the newest Offset+Limit entries can outlive the slicing below.
	keep := 0
	if params.Limit > 0 {
		keep = params.Offset + params.Limit
		if keep < 0 { // overflowed; treat an absurd offset as unbounded
			keep = 0
		}
	}
	selector := newEntrySelector(keep)

	// Determine which directories to scan
	dirs := s.getDirsToScan(params)

	for _, dir := range dirs {
		if err := s.scanDir(dir, params, selector.offer); err != nil {
			return nil, err
		}
	}

	// Newest first, ties in scan order — see sortByTimestamp.
	results := selector.sorted()

	// Apply offset and limit.
	//
	// An offset past the end must yield nothing. It used to yield the FIRST page:
	// the guard was `Offset < len(results)`, so asking for page 9999 of a 3-page
	// result silently skipped the offset entirely and re-served page 1 — a
	// paginating client would loop over the same rows forever instead of
	// terminating. TestOffsetPastTheEndReturnsNothing pins it.
	if params.Offset > 0 {
		if params.Offset >= len(results) {
			results = nil
		} else {
			results = results[params.Offset:]
		}
	}
	if params.Limit > 0 && params.Limit < len(results) {
		results = results[:params.Limit]
	}

	return results, nil
}

// Group aggregates logs by a specific field
func (s *FileStore) Group(params models.QueryParams, groupBy string) ([]models.GroupedLogs, error) {
	entries, err := s.Query(params)
	if err != nil {
		return nil, err
	}

	// Size every bucket before filling any, so each one is allocated exactly once.
	//
	// Appending into a map of slices grew each bucket by doubling, and the biggest
	// bucket here holds ~143k entries: every doubling abandons the previous array,
	// so grouping the 30-day window churned ~136MB of garbage to produce ~48MB of
	// buckets. Counting first costs one extra pass over a slice already in memory.
	keys := make([]string, len(entries))
	counts := make(map[string]int, len(GroupFields))
	for i := range entries {
		keys[i] = getGroupKey(&entries[i], groupBy)
		counts[keys[i]]++
	}

	groups := make(map[string][]models.LogEntry, len(counts))
	for key, n := range counts {
		groups[key] = make([]models.LogEntry, 0, n)
	}
	for i := range entries {
		groups[keys[i]] = append(groups[keys[i]], entries[i])
	}

	results := make([]models.GroupedLogs, 0, len(groups))
	for key, logs := range groups {
		results = append(results, models.GroupedLogs{
			GroupKey: key,
			Count:    len(logs),
			Logs:     logs,
		})
	}

	// Range over a map is randomised, so the group list came back in a different
	// order on every call — the same response reshuffling under an operator who
	// changed nothing. Biggest bucket first, ties by key.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Count != results[j].Count {
			return results[i].Count > results[j].Count
		}
		return results[i].GroupKey < results[j].GroupKey
	})

	return results, nil
}

// Stats returns aggregate statistics
func (s *FileStore) Stats(params models.QueryParams) (*models.Stats, error) {
	entries, err := s.Query(params)
	if err != nil {
		return nil, err
	}

	stats := &models.Stats{
		ByOrch: make(map[string]int),
		ByLevel:  make(map[string]int),
		ByModel:  make(map[string]int),
	}

	var totalLatency int64
	var latencyCount int

	for _, entry := range entries {
		stats.TotalEntries++

		if entry.Orchestrator != "" {
			stats.ByOrch[entry.Orchestrator]++
		}
		if entry.Level != "" {
			stats.ByLevel[entry.Level]++
		}
		if entry.Model != "" {
			stats.ByModel[entry.Model]++
		}

		stats.TotalTokensIn += entry.TokensIn
		stats.TotalTokensOut += entry.TokensOut

		if entry.LatencyMs > 0 {
			totalLatency += entry.LatencyMs
			latencyCount++
		}
	}

	if latencyCount > 0 {
		stats.AvgLatencyMs = float64(totalLatency) / float64(latencyCount)
	}

	return stats, nil
}

// Usage returns aggregated token usage from outbound messages since `from`.
func (s *FileStore) Usage(from time.Time) ([]models.UsageStats, error) {
	params := models.QueryParams{
		Type:  "outbound",
		From:  from,
		Limit: 100000, // no practical limit
	}

	entries, err := s.Query(params)
	if err != nil {
		return nil, err
	}

	type agentKey struct{ agent, orch string }
	agg := make(map[agentKey]*models.UsageStats)

	for _, entry := range entries {
		// Prefer entry-level Stats (new format), fall back to content.meta (legacy)
		var inputTokens, outputTokens int
		var durationMs int64
		var model, orchestrator string

		orchestrator = entry.Orchestrator
		agent := entry.Agent

		if entry.Stats != nil {
			inputTokens = entry.Stats.InputTokens
			outputTokens = entry.Stats.OutputTokens
			durationMs = entry.Stats.DurationMs
			model = entry.Stats.Model
		} else {
			// Legacy: parse content for meta/orchestrator
			contentBytes, err := json.Marshal(entry.Content)
			if err != nil {
				continue
			}
			var content struct {
				Agent        string `json:"agent"`
				Orchestrator string `json:"orchestrator"`
				Meta         *struct {
					InputTokens         int    `json:"input_tokens"`
					OutputTokens        int    `json:"output_tokens"`
					DurationMs          int64  `json:"duration_ms"`
					Model               string `json:"model"`
				} `json:"meta"`
				Stats *struct {
					InputTokens         int    `json:"input_tokens"`
					OutputTokens        int    `json:"output_tokens"`
					DurationMs          int64  `json:"duration_ms"`
					Model               string `json:"model"`
				} `json:"stats"`
			}
			if err := json.Unmarshal(contentBytes, &content); err != nil {
				continue
			}
			meta := content.Meta
			if meta == nil {
				meta = content.Stats
			}
			if meta == nil {
				continue
			}
			inputTokens = meta.InputTokens
			outputTokens = meta.OutputTokens
			durationMs = meta.DurationMs
			model = meta.Model
			if orchestrator == "" {
				orchestrator = content.Orchestrator
			}
			if agent == "" {
				agent = content.Agent
			}
		}

		if model == "" {
			model = entry.Model
		}

		k := agentKey{agent, orchestrator}
		stats, ok := agg[k]
		if !ok {
			stats = &models.UsageStats{
				Agent:        agent,
				Orchestrator: orchestrator,
				Model:        model,
			}
			agg[k] = stats
		}

		stats.Messages++
		stats.InputTokens += inputTokens
		stats.OutputTokens += outputTokens
		stats.TotalTokens += inputTokens + outputTokens
		stats.DurationMs += durationMs
	}

	out := make([]models.UsageStats, 0, len(agg))
	for _, s := range agg {
		out = append(out, *s)
	}
	return out, nil
}

// Pricing per 1M tokens (input, output, cache_read, cache_write)
var modelPricing = map[string][4]float64{
	"claude-opus-4-6":      {15.0, 75.0, 3.75, 18.75},
	"claude-opus-4-5":      {15.0, 75.0, 3.75, 18.75},
	"claude-opus-4":        {15.0, 75.0, 3.75, 18.75},
	"claude-opus-3-5":      {15.0, 75.0, 3.75, 18.75},
	"claude-sonnet-4-5":    {3.0, 15.0, 0.30, 3.75},
	"claude-sonnet-4":      {3.0, 15.0, 0.30, 3.75},
	"claude-sonnet-3-5":    {3.0, 15.0, 0.30, 3.75},
	"claude-sonnet-3":      {3.0, 15.0, 0.30, 3.75},
	"claude-haiku-3-5":     {0.25, 1.25, 0.03, 0.30},
	"claude-haiku-3":       {0.25, 1.25, 0.03, 0.30},
}

// normalizeModel converts various model name formats to a standard form
func normalizeModel(model string) string {
	model = strings.ToLower(model)
	// Handle common variations
	model = strings.ReplaceAll(model, "anthropic/", "")
	model = strings.ReplaceAll(model, "claude-3-5-", "claude-")
	model = strings.ReplaceAll(model, "claude-3-", "claude-")
	return model
}

// calculateCost estimates the cost based on token usage and model
func calculateCost(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int) float64 {
	normalizedModel := normalizeModel(model)
	
	// Find matching pricing
	var pricing [4]float64
	found := false
	for modelPattern, prices := range modelPricing {
		if strings.Contains(normalizedModel, strings.TrimPrefix(modelPattern, "claude-")) {
			pricing = prices
			found = true
			break
		}
	}
	
	// Default to sonnet pricing if no match
	if !found {
		pricing = [4]float64{3.0, 15.0, 0.30, 3.75}
	}
	
	cost := float64(inputTokens)/1_000_000*pricing[0] +
		float64(outputTokens)/1_000_000*pricing[1] +
		float64(cacheReadTokens)/1_000_000*pricing[2] +
		float64(cacheWriteTokens)/1_000_000*pricing[3]
	
	return cost
}

// MaxUsage returns comprehensive Max subscription usage for a billing period
func (s *FileStore) MaxUsage(from, to time.Time) (*models.MaxUsageResponse, error) {
	params := models.QueryParams{
		Type:  "outbound",
		From:  from,
		To:    to,
		Limit: 500000, // no practical limit
	}

	entries, err := s.Query(params)
	if err != nil {
		return nil, err
	}

	// Track 429 errors for rate limit info
	params429 := models.QueryParams{
		Level: "error",
		From:  from,
		To:    to,
		Limit: 10000,
	}
	entries429, _ := s.Query(params429)

	response := &models.MaxUsageResponse{
		PeriodStart:    from.Format(time.RFC3339),
		PeriodEnd:      to.Format(time.RFC3339),
		ByModel:        make(map[string]models.MaxUsageByModel),
		ByOrchestrator: make(map[string]models.MaxUsageByOrchestrator),
		ByDay:          []models.MaxUsageByDay{},
		RateLimits:     models.MaxUsageRateLimits{},
	}

	// Aggregate data structures
	byDayMap := make(map[string]*models.MaxUsageByDay)
	var last429 time.Time

	// Process outbound messages for usage
	for _, entry := range entries {
		var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int
		var model, orchestrator string

		orchestrator = entry.Orchestrator

		if entry.Stats != nil {
			inputTokens = entry.Stats.InputTokens
			outputTokens = entry.Stats.OutputTokens
			cacheReadTokens = entry.Stats.CacheReadTokens
			cacheWriteTokens = entry.Stats.CacheCreationTokens + entry.Stats.CacheWriteTokens
			model = entry.Stats.Model
		} else {
			// Legacy: parse content for meta
			contentBytes, err := json.Marshal(entry.Content)
			if err != nil {
				continue
			}
			var content struct {
				Orchestrator string `json:"orchestrator"`
				Meta         *struct {
					InputTokens         int    `json:"input_tokens"`
					OutputTokens        int    `json:"output_tokens"`
					CacheReadTokens     int    `json:"cache_read_tokens"`
					CacheCreationTokens int    `json:"cache_creation_tokens"`
					Model               string `json:"model"`
				} `json:"meta"`
				Stats *struct {
					InputTokens         int    `json:"input_tokens"`
					OutputTokens        int    `json:"output_tokens"`
					CacheReadTokens     int    `json:"cache_read_tokens"`
					CacheCreationTokens int    `json:"cache_creation_tokens"`
					Model               string `json:"model"`
				} `json:"stats"`
			}
			if err := json.Unmarshal(contentBytes, &content); err != nil {
				continue
			}
			meta := content.Meta
			if meta == nil {
				meta = content.Stats
			}
			if meta == nil {
				continue
			}
			inputTokens = meta.InputTokens
			outputTokens = meta.OutputTokens
			cacheReadTokens = meta.CacheReadTokens
			cacheWriteTokens = meta.CacheCreationTokens
			model = meta.Model
			if orchestrator == "" {
				orchestrator = content.Orchestrator
			}
		}

		if model == "" {
			model = entry.Model
		}
		if orchestrator == "" {
			orchestrator = "unknown"
		}

		// Calculate cost for this entry
		cost := calculateCost(model, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)

		// Update totals
		response.Totals.InputTokens += inputTokens
		response.Totals.OutputTokens += outputTokens
		response.Totals.CacheReadTokens += cacheReadTokens
		response.Totals.CacheWriteTokens += cacheWriteTokens
		response.Totals.TotalTokens += inputTokens + outputTokens + cacheReadTokens + cacheWriteTokens
		response.Totals.APICalls++
		response.Totals.EstimatedCost += cost

		// Update by_model
		modelStats := response.ByModel[model]
		modelStats.InputTokens += inputTokens
		modelStats.OutputTokens += outputTokens
		modelStats.CacheReadTokens += cacheReadTokens
		modelStats.CacheWriteTokens += cacheWriteTokens
		modelStats.APICalls++
		modelStats.EstimatedCost += cost
		response.ByModel[model] = modelStats

		// Update by_orchestrator
		orchStats := response.ByOrchestrator[orchestrator]
		orchStats.InputTokens += inputTokens
		orchStats.OutputTokens += outputTokens
		orchStats.CacheReadTokens += cacheReadTokens
		orchStats.CacheWriteTokens += cacheWriteTokens
		orchStats.APICalls++
		orchStats.EstimatedCost += cost
		response.ByOrchestrator[orchestrator] = orchStats

		// Update by_day
		dayKey := entry.Timestamp.Format("2006-01-02")
		dayStats, ok := byDayMap[dayKey]
		if !ok {
			dayStats = &models.MaxUsageByDay{Date: dayKey}
			byDayMap[dayKey] = dayStats
		}
		dayStats.InputTokens += inputTokens
		dayStats.OutputTokens += outputTokens
		dayStats.CacheReadTokens += cacheReadTokens
		dayStats.CacheWriteTokens += cacheWriteTokens
		dayStats.APICalls++
		dayStats.Cost += cost
	}

	// Process 429 errors for rate limits
	for _, entry := range entries429 {
		// Check if this is a 429 error
		contentBytes, err := json.Marshal(entry.Content)
		if err != nil {
			continue
		}

		var content struct {
			StatusCode int    `json:"status_code"`
			Error      string `json:"error"`
			Message    string `json:"message"`
		}

		if err := json.Unmarshal(contentBytes, &content); err != nil {
			continue
		}

		is429 := content.StatusCode == 429 ||
			strings.Contains(content.Error, "429") ||
			strings.Contains(content.Message, "429") ||
			strings.Contains(content.Error, "rate limit") ||
			strings.Contains(content.Message, "rate limit") ||
			strings.Contains(content.Error, "overloaded") ||
			strings.Contains(content.Message, "overloaded")

		if is429 {
			response.RateLimits.Count429++
			if entry.Timestamp.After(last429) {
				last429 = entry.Timestamp
			}
		}
	}

	// Set last 429 timestamp
	if !last429.IsZero() {
		response.RateLimits.Last429 = last429.Format(time.RFC3339)
	}

	// Convert byDayMap to sorted slice
	for date, stats := range byDayMap {
		response.ByDay = append(response.ByDay, *stats)
		_ = date // avoid unused variable error
	}

	// Sort by_day by date
	sortByDay(response.ByDay)

	return response, nil
}

// sortByDay sorts the by_day slice oldest-first by date.
//
// Same hand-rolled O(n^2) shape as sortByTimestamp was; n is only the number of
// days in the billing period, so this one was never a real cost — but it is the
// same defect, and leaving it would invite the next person to copy it.
func sortByDay(days []models.MaxUsageByDay) {
	sort.Slice(days, func(i, j int) bool {
		return days[i].Date < days[j].Date
	})
}

// RateLimits returns recent 429 error events
func (s *FileStore) RateLimits(from time.Time, limit int) (*models.RateLimitsResponse, error) {
	if limit == 0 {
		limit = 100
	}

	params := models.QueryParams{
		Level: "error",
		From:  from,
		Limit: 10000, // Scan more to filter for 429s
	}

	entries, err := s.Query(params)
	if err != nil {
		return nil, err
	}

	response := &models.RateLimitsResponse{
		Events: []models.RateLimitEvent{},
	}

	for _, entry := range entries {
		contentBytes, err := json.Marshal(entry.Content)
		if err != nil {
			continue
		}

		var content struct {
			StatusCode   int    `json:"status_code"`
			Error        string `json:"error"`
			Message      string `json:"message"`
			Model        string `json:"model"`
			Orchestrator string `json:"orchestrator"`
		}

		if err := json.Unmarshal(contentBytes, &content); err != nil {
			continue
		}

		is429 := content.StatusCode == 429 ||
			strings.Contains(content.Error, "429") ||
			strings.Contains(content.Message, "429") ||
			strings.Contains(content.Error, "rate limit") ||
			strings.Contains(content.Message, "rate limit") ||
			strings.Contains(content.Error, "overloaded") ||
			strings.Contains(content.Message, "overloaded")

		if is429 {
			orch := entry.Orchestrator
			if orch == "" {
				orch = content.Orchestrator
			}
			mdl := entry.Model
			if mdl == "" {
				mdl = content.Model
			}
			event := models.RateLimitEvent{
				Timestamp:    entry.Timestamp.Format(time.RFC3339),
				Model:        mdl,
				Orchestrator: orch,
				Message:      content.Error,
			}
			if event.Message == "" {
				event.Message = content.Message
			}
			response.Events = append(response.Events, event)
			response.Total++

			if len(response.Events) >= limit {
				break
			}
		}
	}

	return response, nil
}

// Get retrieves a single log by ID
func (s *FileStore) Get(id string) (*models.LogEntry, error) {
	s.mu.RLock()
	path, ok := s.index[id]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("log not found: %s", id)
	}

	// Scan the file to find the entry
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry models.LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.ID == id {
			return &entry, nil
		}
	}

	return nil, fmt.Errorf("log not found in file: %s", id)
}

// Delete removes logs matching params
func (s *FileStore) Delete(params models.QueryParams) (int, error) {
	// For file-based store, deletion is complex
	// For now, return error - implement later if needed
	return 0, fmt.Errorf("delete not implemented for file store")
}

// Helper functions

func (s *FileStore) buildIndex() error {
	return filepath.Walk(s.baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB max line
		for scanner.Scan() {
			var entry models.LogEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}
			if entry.ID != "" {
				s.index[entry.ID] = path
			}
		}

		return nil
	})
}

func (s *FileStore) getDirsToScan(params models.QueryParams) []string {
	var dirs []string

	if !params.From.IsZero() {
		// Scan from start date to end (or now)
		end := params.To
		if end.IsZero() {
			end = time.Now()
		}
		for d := params.From; !d.After(end); d = d.AddDate(0, 0, 1) {
			dir := filepath.Join(s.baseDir, d.Format("2006-01-02"))
			if _, err := os.Stat(dir); err == nil {
				dirs = append(dirs, dir)
			}
		}
	} else if !params.To.IsZero() {
		// Scan up to end date (last 30 days before To)
		start := params.To.AddDate(0, 0, -30)
		for d := start; !d.After(params.To); d = d.AddDate(0, 0, 1) {
			dir := filepath.Join(s.baseDir, d.Format("2006-01-02"))
			if _, err := os.Stat(dir); err == nil {
				dirs = append(dirs, dir)
			}
		}
	} else {
		// Scan recent directories (last 30 days)
		now := time.Now()
		for i := 0; i < 30; i++ {
			date := now.AddDate(0, 0, -i)
			dir := filepath.Join(s.baseDir, date.Format("2006-01-02"))
			if _, err := os.Stat(dir); err == nil {
				dirs = append(dirs, dir)
			}
		}
	}

	return dirs
}

// scanDir passes every entry in dir matching params to visit.
//
// It streams rather than returning a slice so that a bounded Query never holds a
// whole day's logs at once: the selector on the other end of visit drops what it
// cannot use as each line is parsed.
func (s *FileStore) scanDir(dir string, params models.QueryParams, visit func(models.LogEntry)) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return err
	}

	// Filter by orchestrator if specified
	if params.Orchestrator != "" {
		targetFile := params.Orchestrator + ".jsonl"
		files = []string{filepath.Join(dir, targetFile)}
	}

	for _, file := range files {
		if err := s.scanFile(file, params, visit); err != nil {
			// A file that isn't there is not an error: the orchestrator filter above
			// names one by convention without checking it exists. Anything else —
			// notably a truncated read — is, and must not be swallowed, or the
			// silent-truncation guard in scanFile guards nothing.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
	}

	return nil
}

// scanFile passes every entry in path matching params to visit.
func (s *FileStore) scanFile(path string, params models.QueryParams, visit func(models.LogEntry)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		var entry models.LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		// Apply filters
		if !matchesParams(&entry, params) {
			continue
		}

		visit(entry)
	}

	// A line longer than the buffer above stops the scan and is otherwise
	// INDISTINGUISHABLE FROM EOF: bufio.Scanner just stops returning tokens. Left
	// unchecked, one oversized line silently hides every log written after it in
	// that file, and the query reports the truncated result as a complete one.
	// (No line in the live corpus is anywhere near the cap today — the longest is
	// ~36KB — which is exactly why this would go unnoticed if it ever changed.)
	//
	// A malformed line is different, and still skipped above: that is one unusable
	// record, not a truncated file.
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}

	return nil
}

func matchesParams(entry *models.LogEntry, params models.QueryParams) bool {
	if params.Orchestrator != "" && entry.Orchestrator != params.Orchestrator {
		return false
	}
	if params.Agent != "" && entry.Agent != params.Agent {
		return false
	}
	if params.Channel != "" && entry.Channel != params.Channel {
		return false
	}
	if params.SessionID != "" && entry.SessionID != params.SessionID {
		return false
	}
	if params.TurnID != "" && entry.TurnID != params.TurnID {
		return false
	}
	if params.Model != "" && entry.Model != params.Model {
		return false
	}
	if params.Level != "" && entry.Level != params.Level {
		return false
	}
	if params.Type != "" && entry.Type != params.Type {
		return false
	}
	if !params.From.IsZero() && entry.Timestamp.Before(params.From) {
		return false
	}
	if !params.To.IsZero() && entry.Timestamp.After(params.To) {
		return false
	}
	return true
}

// GroupFields lists the fields Group can group by. It is the single source of
// truth for callers that need to validate a requested group field: every entry
// here must have a case in getGroupKey, or grouping silently collapses every
// log into one "unknown" bucket. GroupFieldsAreHandled pins that.
var GroupFields = []string{
	"orchestrator", "agent", "channel",
	"model", "level", "type",
	"session", "hour", "day",
}

// IsGroupField reports whether groupBy is a field Group understands.
func IsGroupField(groupBy string) bool {
	for _, f := range GroupFields {
		if f == groupBy {
			return true
		}
	}
	return false
}

func getGroupKey(entry *models.LogEntry, groupBy string) string {
	switch groupBy {
	case "orchestrator":
		return entry.Orchestrator
	case "agent":
		return entry.Agent
	case "channel":
		return entry.Channel
	case "model":
		return entry.Model
	case "level":
		return entry.Level
	case "type":
		return entry.Type
	case "session":
		return entry.SessionID
	case "hour":
		return entry.Timestamp.Format("2006-01-02T15")
	case "day":
		return entry.Timestamp.Format("2006-01-02")
	default:
		return "unknown"
	}
}

// sortByTimestamp orders entries newest-first.
//
// This was a hand-rolled O(n^2) selection sort ("optimize later if needed", and
// nobody did) from logstack's first commit until 2026-07-12, and it was the whole
// reason an unscoped query took two minutes. Query sorts every entry it
// materialised, and the default 30-day window holds ~196k of them: ~1.9e10
// comparisons, ~118s of pure CPU. Reading and parsing all 88MB off disk takes
// ~2s. The scan was never the expensive part.
//
// sort.SliceStable, not sort.Slice: Query applies Offset/Limit *after* sorting, so
// entries sharing a timestamp need a defined order or paging through them could
// skip or repeat rows. The old sort left ties in an arbitrary order.
func sortByTimestamp(entries []models.LogEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
}

// entrySelector collects the entries a Query will return.
//
// It exists to make a bounded query cost what it asked for. Its whole contract is
// that it produces EXACTLY what "materialise everything, sortByTimestamp, then
// slice" produced, while holding at most `keep` entries at a time — so the tie
// handling has to match sortByTimestamp's, not merely resemble it.
//
// sortByTimestamp is a *stable* descending sort, so the total order it defines is
// (timestamp descending, then scan order ascending): among entries sharing a
// timestamp, the one parsed first wins. seq records that scan order, and it is
// what lets a heap reproduce a stable sort's top-K. Without it, entries sharing a
// timestamp would be kept or dropped arbitrarily at the boundary, and paging
// through them could skip or repeat rows — the exact hazard sortByTimestamp's own
// comment says it uses SliceStable to avoid.
type entrySelector struct {
	keep int   // 0 = unbounded: keep everything
	seq  int64 // entries offered so far; also each entry's scan position
	all  []models.LogEntry
	best rankedEntryHeap
}

// rankedEntry is an entry plus the position it was scanned at.
type rankedEntry struct {
	entry models.LogEntry
	seq   int64
}

func newEntrySelector(keep int) *entrySelector {
	// Deliberately no preallocation to `keep`: MaxUsage passes Limit=500000 as
	// "no practical limit", and reserving half a million entries up front would
	// cost more than the scan it is meant to bound.
	return &entrySelector{keep: keep}
}

// offer presents one matching entry to the selector, which keeps it or drops it.
func (s *entrySelector) offer(entry models.LogEntry) {
	seq := s.seq
	s.seq++

	if s.keep == 0 {
		s.all = append(s.all, entry)
		return
	}

	if len(s.best) < s.keep {
		heap.Push(&s.best, rankedEntry{entry: entry, seq: seq})
		return
	}

	// s.best[0] is the weakest entry kept so far. Note an entry can never displace
	// one with the same timestamp: seq only grows, and the earlier scan position
	// wins ties, which is precisely what the stable sort would have done.
	if beatsRankedEntry(entry, seq, s.best[0]) {
		s.best[0] = rankedEntry{entry: entry, seq: seq}
		heap.Fix(&s.best, 0)
	}
}

// sorted returns the kept entries newest-first, ties in scan order.
func (s *entrySelector) sorted() []models.LogEntry {
	if s.keep == 0 {
		sortByTimestamp(s.all)
		return s.all
	}

	sort.Slice(s.best, func(i, j int) bool {
		return beatsRankedEntry(s.best[i].entry, s.best[i].seq, s.best[j])
	})

	results := make([]models.LogEntry, len(s.best))
	for i, ranked := range s.best {
		results[i] = ranked.entry
	}
	return results
}

// beatsRankedEntry reports whether an entry scanned at seq outranks other under
// the order sortByTimestamp defines: newer timestamp first, ties to whichever was
// scanned first.
func beatsRankedEntry(entry models.LogEntry, seq int64, other rankedEntry) bool {
	if !entry.Timestamp.Equal(other.entry.Timestamp) {
		return entry.Timestamp.After(other.entry.Timestamp)
	}
	return seq < other.seq
}

// rankedEntryHeap is a min-heap whose root is the entry closest to being dropped:
// the oldest, and among equal timestamps the one scanned last.
type rankedEntryHeap []rankedEntry

func (h rankedEntryHeap) Len() int { return len(h) }

func (h rankedEntryHeap) Less(i, j int) bool {
	return beatsRankedEntry(h[j].entry, h[j].seq, h[i])
}

func (h rankedEntryHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *rankedEntryHeap) Push(x any) { *h = append(*h, x.(rankedEntry)) }

func (h *rankedEntryHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
