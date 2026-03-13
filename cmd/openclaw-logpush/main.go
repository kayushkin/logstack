package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// OpenClaw JSONL entry
type ocEntry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type ocMessage struct {
	Role        string          `json:"role"`
	Content     json.RawMessage `json:"content"`
	Model       string          `json:"model"`
	Provider    string          `json:"provider"`
	API         string          `json:"api"`
	Usage       *ocUsage        `json:"usage"`
	StopReason  string          `json:"stopReason"`
	Timestamp   int64           `json:"timestamp"`
}

type ocUsage struct {
	Input      int      `json:"input"`
	Output     int      `json:"output"`
	CacheRead  int      `json:"cacheRead"`
	CacheWrite int      `json:"cacheWrite"`
	Total      int      `json:"totalTokens"`
	Cost       *ocCost  `json:"cost"`
}

type ocCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// Logstack entry
type logEntry struct {
	ID        string                 `json:"id"`
	Timestamp string                 `json:"timestamp"`
	Source    string                 `json:"source"`
	Agent    string                 `json:"agent,omitempty"`
	Channel  string                 `json:"channel,omitempty"`
	SessionID string                `json:"session_id,omitempty"`
	Model    string                 `json:"model,omitempty"`
	Level    string                 `json:"level"`
	Type     string                 `json:"type"`
	Content  map[string]interface{} `json:"content"`
	TokensIn  int                   `json:"tokens_in,omitempty"`
	TokensOut int                   `json:"tokens_out,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Cursor state
type cursorState struct {
	Offset int64 `json:"offset"`
}

var authorRe = regexp.MustCompile(`^\[(\w+)\]\s*`)

func extractText(raw json.RawMessage) string {
	// Try string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try array of content blocks
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}

	return ""
}

func extractThinking(raw json.RawMessage) string {
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "thinking" && b.Thinking != "" {
				return b.Thinking
			}
		}
	}
	return ""
}

func convertEntry(e ocEntry, agent, sessionID string) *logEntry {
	if e.Type != "message" {
		return nil
	}

	var msg ocMessage
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		return nil
	}

	text := extractText(msg.Content)
	if text == "" {
		return nil
	}

	entryType := "outbound"
	author := agent
	if msg.Role == "user" {
		entryType = "inbound"
		author = "user"
		if m := authorRe.FindStringSubmatch(text); len(m) > 1 {
			author = m[1]
		}
	}

	le := &logEntry{
		ID:        sessionID + "-" + e.ID,
		Timestamp: e.Timestamp,
		Source:    "openclaw",
		Agent:    agent,
		Channel:  "webchat",
		SessionID: sessionID,
		Model:    msg.Model,
		Level:    "info",
		Type:     entryType,
		Content: map[string]interface{}{
			"text":         text,
			"author":       author,
			"agent":        agent,
			"orchestrator": "openclaw",
			"stream":       "done",
		},
	}

	// Add thinking if present
	if thinking := extractThinking(msg.Content); thinking != "" {
		le.Content["thinking"] = thinking
	}

	if msg.Usage != nil {
		le.TokensIn = msg.Usage.Input
		le.TokensOut = msg.Usage.Output
		le.Metadata = map[string]interface{}{
			"cache_read":  msg.Usage.CacheRead,
			"cache_write": msg.Usage.CacheWrite,
		}
		if msg.Usage.Cost != nil {
			le.Metadata["cost"] = msg.Usage.Cost.Total
		}
		if msg.StopReason != "" {
			le.Metadata["stop_reason"] = msg.StopReason
		}
		if msg.Provider != "" {
			le.Metadata["provider"] = msg.Provider
		}
	}

	return le
}

func pushBatch(url string, entries []logEntry) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}

	resp, err := http.Post(url+"/api/v1/logs/batch", "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("logstack returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func loadCursors(path string) map[string]cursorState {
	cursors := make(map[string]cursorState)
	data, err := os.ReadFile(path)
	if err != nil {
		return cursors
	}
	json.Unmarshal(data, &cursors)
	return cursors
}

func saveCursors(path string, cursors map[string]cursorState) {
	os.MkdirAll(filepath.Dir(path), 0755)
	data, _ := json.MarshalIndent(cursors, "", "  ")
	os.WriteFile(path, data, 0644)
}

// Map openclaw agent directory names to display names
var agentNameMap = map[string]string{
	"main": "claxon",
}

func mapAgentName(dirName string) string {
	if mapped, ok := agentNameMap[dirName]; ok {
		return mapped
	}
	return dirName
}

func discoverSessions(openclawDir string) []struct{ agent, sessionID, path string } {
	var sessions []struct{ agent, sessionID, path string }

	agentsDir := filepath.Join(openclawDir, "agents")
	agents, err := os.ReadDir(agentsDir)
	if err != nil {
		return sessions
	}

	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		sessDir := filepath.Join(agentsDir, a.Name(), "sessions")
		files, err := os.ReadDir(sessDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			sessionID := strings.TrimSuffix(f.Name(), ".jsonl")
			sessions = append(sessions, struct{ agent, sessionID, path string }{
				agent:     mapAgentName(a.Name()),
				sessionID: sessionID,
				path:      filepath.Join(sessDir, f.Name()),
			})
		}
	}
	return sessions
}

func processFile(path, agent, sessionID string, offset int64, dryRun bool, logstackURL string) (int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, 0, err
	}
	defer f.Close()

	// Get file size
	info, err := f.Stat()
	if err != nil {
		return offset, 0, err
	}

	if info.Size() <= offset {
		return offset, 0, nil
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, 0, err
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB lines
	var batch []logEntry
	count := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var e ocEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}

		le := convertEntry(e, agent, sessionID)
		if le == nil {
			continue
		}

		if dryRun {
			role := "assistant"
			if le.Type == "inbound" {
				role = "user"
			}
			text := ""
			if t, ok := le.Content["text"].(string); ok {
				if len(t) > 80 {
					t = t[:80] + "..."
				}
				text = t
			}
			log.Printf("[dry-run] %s/%s %s: %s", agent, sessionID[:8], role, text)
		} else {
			batch = append(batch, *le)
		}
		count++
	}

	// Push batch
	if !dryRun && len(batch) > 0 {
		// Push in chunks of 50
		for i := 0; i < len(batch); i += 50 {
			end := i + 50
			if end > len(batch) {
				end = len(batch)
			}
			if err := pushBatch(logstackURL, batch[i:end]); err != nil {
				return offset, 0, fmt.Errorf("push batch: %w", err)
			}
		}
	}

	newOffset, _ := f.Seek(0, io.SeekCurrent)
	return newOffset, count, nil
}

func main() {
	logstackURL := flag.String("logstack-url", "http://localhost:8088", "logstack base URL")
	openclawDir := flag.String("openclaw-dir", "", "OpenClaw data dir (default: ~/.openclaw)")
	interval := flag.Duration("interval", 5*time.Second, "poll interval")
	backfill := flag.Bool("backfill", false, "push all existing messages on first run")
	dryRun := flag.Bool("dry-run", false, "print what would be pushed")
	flag.Parse()

	if *openclawDir == "" {
		home, _ := os.UserHomeDir()
		*openclawDir = filepath.Join(home, ".openclaw")
	}

	home, _ := os.UserHomeDir()
	cursorPath := filepath.Join(home, ".config", "openclaw-logpush", "cursors.json")
	cursors := loadCursors(cursorPath)

	log.SetFlags(log.Ltime)
	log.Printf("openclaw-logpush starting (logstack=%s, dir=%s, interval=%s, backfill=%v, dry-run=%v)",
		*logstackURL, *openclawDir, *interval, *backfill, *dryRun)

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Initial scan — if not backfilling, set cursors to current EOF
	if !*backfill {
		sessions := discoverSessions(*openclawDir)
		for _, s := range sessions {
			key := s.agent + "/" + filepath.Base(s.path)
			if _, ok := cursors[key]; !ok {
				info, err := os.Stat(s.path)
				if err == nil {
					cursors[key] = cursorState{Offset: info.Size()}
				}
			}
		}
		if !*dryRun {
			saveCursors(cursorPath, cursors)
		}
		log.Printf("skipping existing messages (use --backfill to push history)")
	}

	poll := func() {
		sessions := discoverSessions(*openclawDir)
		totalNew := 0

		for _, s := range sessions {
			key := s.agent + "/" + filepath.Base(s.path)
			cursor := cursors[key]

			newOffset, count, err := processFile(s.path, s.agent, s.sessionID, cursor.Offset, *dryRun, *logstackURL)
			if err != nil {
				log.Printf("error processing %s: %v", key, err)
				continue
			}

			if count > 0 {
				log.Printf("pushed %d messages from %s", count, key)
				totalNew += count
			}

			if newOffset != cursor.Offset {
				cursors[key] = cursorState{Offset: newOffset}
			}
		}

		if totalNew > 0 && !*dryRun {
			saveCursors(cursorPath, cursors)
		}
	}

	// First poll
	poll()

	if *dryRun {
		log.Printf("dry-run complete")
		return
	}

	// Poll loop
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			poll()
		case <-sigCh:
			log.Printf("shutting down")
			saveCursors(cursorPath, cursors)
			return
		}
	}
}
