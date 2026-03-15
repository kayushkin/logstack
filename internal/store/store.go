package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	mu      sync.RWMutex

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

	// Organize by date and source
	// Structure: baseDir/YYYY-MM-DD/source.jsonl
	dateStr := entry.Timestamp.Format("2006-01-02")
	filename := fmt.Sprintf("%s.jsonl", entry.Source)
	if entry.Source == "" {
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

// Query searches for logs matching the given parameters
func (s *FileStore) Query(params models.QueryParams) ([]models.LogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []models.LogEntry

	// Determine which directories to scan
	dirs := s.getDirsToScan(params)

	for _, dir := range dirs {
		entries, err := s.scanDir(dir, params)
		if err != nil {
			return nil, err
		}
		results = append(results, entries...)
	}

	// Sort by timestamp (newest first)
	sortByTimestamp(results)

	// Apply offset and limit
	if params.Offset > 0 && params.Offset < len(results) {
		results = results[params.Offset:]
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

	groups := make(map[string][]models.LogEntry)

	for _, entry := range entries {
		key := getGroupKey(&entry, groupBy)
		groups[key] = append(groups[key], entry)
	}

	var results []models.GroupedLogs
	for key, logs := range groups {
		results = append(results, models.GroupedLogs{
			GroupKey: key,
			Count:    len(logs),
			Logs:     logs,
		})
	}

	return results, nil
}

// Stats returns aggregate statistics
func (s *FileStore) Stats(params models.QueryParams) (*models.Stats, error) {
	entries, err := s.Query(params)
	if err != nil {
		return nil, err
	}

	stats := &models.Stats{
		BySource: make(map[string]int),
		ByLevel:  make(map[string]int),
		ByModel:  make(map[string]int),
	}

	var totalLatency int64
	var latencyCount int

	for _, entry := range entries {
		stats.TotalEntries++

		if entry.Source != "" {
			stats.BySource[entry.Source]++
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
		// Parse content to extract meta (content is interface{})
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
				CacheReadTokens     int    `json:"cache_read_tokens"`
				CacheCreationTokens int    `json:"cache_creation_tokens"`
			} `json:"meta"`
		}

		if err := json.Unmarshal(contentBytes, &content); err != nil {
			continue
		}

		if content.Meta == nil {
			continue
		}

		agent := content.Agent
		if agent == "" {
			agent = entry.Agent
		}

		k := agentKey{agent, content.Orchestrator}
		stats, ok := agg[k]
		if !ok {
			stats = &models.UsageStats{
				Agent:        agent,
				Orchestrator: content.Orchestrator,
				Model:        content.Meta.Model,
			}
			agg[k] = stats
		}

		stats.Messages++
		stats.InputTokens += content.Meta.InputTokens
		stats.OutputTokens += content.Meta.OutputTokens
		stats.TotalTokens += content.Meta.InputTokens + content.Meta.OutputTokens
		stats.DurationMs += content.Meta.DurationMs
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
				CacheReadTokens     int    `json:"cache_read_tokens"`
				CacheCreationTokens int    `json:"cache_creation_tokens"`
				Model               string `json:"model"`
			} `json:"meta"`
		}

		if err := json.Unmarshal(contentBytes, &content); err != nil {
			continue
		}

		if content.Meta == nil {
			continue
		}

		model := content.Meta.Model
		if model == "" {
			model = entry.Model
		}
		orchestrator := content.Orchestrator
		if orchestrator == "" {
			orchestrator = "unknown"
		}

		inputTokens := content.Meta.InputTokens
		outputTokens := content.Meta.OutputTokens
		cacheReadTokens := content.Meta.CacheReadTokens
		cacheWriteTokens := content.Meta.CacheCreationTokens

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

// sortByDay sorts the by_day slice by date
func sortByDay(days []models.MaxUsageByDay) {
	for i := 0; i < len(days)-1; i++ {
		for j := i + 1; j < len(days); j++ {
			if days[i].Date > days[j].Date {
				days[i], days[j] = days[j], days[i]
			}
		}
	}
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
			event := models.RateLimitEvent{
				Timestamp:    entry.Timestamp.Format(time.RFC3339),
				Model:        content.Model,
				Orchestrator: content.Orchestrator,
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
		// Scan recent directories (last 7 days)
		now := time.Now()
		for i := 0; i < 7; i++ {
			date := now.AddDate(0, 0, -i)
			dir := filepath.Join(s.baseDir, date.Format("2006-01-02"))
			if _, err := os.Stat(dir); err == nil {
				dirs = append(dirs, dir)
			}
		}
	}

	return dirs
}

func (s *FileStore) scanDir(dir string, params models.QueryParams) ([]models.LogEntry, error) {
	var entries []models.LogEntry

	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}

	// Filter by source if specified
	if params.Source != "" {
		targetFile := params.Source + ".jsonl"
		files = []string{filepath.Join(dir, targetFile)}
	}

	for _, file := range files {
		ents, err := s.scanFile(file, params)
		if err != nil {
			continue
		}
		entries = append(entries, ents...)
	}

	return entries, nil
}

func (s *FileStore) scanFile(path string, params models.QueryParams) ([]models.LogEntry, error) {
	var entries []models.LogEntry

	f, err := os.Open(path)
	if err != nil {
		return nil, err
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

		entries = append(entries, entry)
	}

	return entries, nil
}

func matchesParams(entry *models.LogEntry, params models.QueryParams) bool {
	if params.Source != "" && entry.Source != params.Source {
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

func getGroupKey(entry *models.LogEntry, groupBy string) string {
	switch groupBy {
	case "source":
		return entry.Source
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

func sortByTimestamp(entries []models.LogEntry) {
	// Simple bubble sort for now (optimize later if needed)
	for i := 0; i < len(entries)-1; i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Timestamp.Before(entries[j].Timestamp) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}
