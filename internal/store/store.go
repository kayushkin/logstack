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
	"github.com/kayushkin/logstack/internal/models"
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

	if !params.From.IsZero() && !params.To.IsZero() {
		// Scan specific date range
		for d := params.From; d.Before(params.To) || d.Equal(params.To); d = d.AddDate(0, 0, 1) {
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
