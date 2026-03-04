package format

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kayushkin/logstack/models"
)

// Formatter converts logs to different output formats
type Formatter struct{}

// NewFormatter creates a new formatter
func NewFormatter() *Formatter {
	return &Formatter{}
}

// JSON formats a log entry as JSON
func (f *Formatter) JSON(entry *models.LogEntry) (string, error) {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// JSONL formats a log entry as a single JSON line
func (f *Formatter) JSONL(entry *models.LogEntry) (string, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Text formats a log entry as human-readable text
func (f *Formatter) Text(entry *models.LogEntry) string {
	var sb strings.Builder

	// Timestamp and level
	sb.WriteString(fmt.Sprintf("[%s] %s ", entry.Timestamp.Format("2006-01-02 15:04:05"), strings.ToUpper(entry.Level)))

	// Source and agent
	if entry.Source != "" {
		sb.WriteString(fmt.Sprintf("%s", entry.Source))
		if entry.Agent != "" {
			sb.WriteString(fmt.Sprintf("/%s", entry.Agent))
		}
		sb.WriteString(" ")
	}

	// Type
	if entry.Type != "" {
		sb.WriteString(fmt.Sprintf("[%s] ", entry.Type))
	}

	// Content
	switch v := entry.Content.(type) {
	case string:
		sb.WriteString(v)
	case map[string]interface{}:
		// Pretty print if it's a structured content
		if msg, ok := v["message"].(string); ok {
			sb.WriteString(msg)
		} else {
			data, _ := json.Marshal(v)
			sb.WriteString(string(data))
		}
	default:
		data, _ := json.Marshal(v)
		sb.WriteString(string(data))
	}

	// Metrics
	if entry.TokensIn > 0 || entry.TokensOut > 0 {
		sb.WriteString(fmt.Sprintf(" [tokens: in=%d out=%d]", entry.TokensIn, entry.TokensOut))
	}
	if entry.LatencyMs > 0 {
		sb.WriteString(fmt.Sprintf(" [latency: %dms]", entry.LatencyMs))
	}

	// Error
	if entry.Error != "" {
		sb.WriteString(fmt.Sprintf(" [error: %s]", entry.Error))
	}

	// Model
	if entry.Model != "" {
		sb.WriteString(fmt.Sprintf(" [model: %s]", entry.Model))
	}

	return sb.String()
}

// Summary formats a brief one-line summary
func (f *Formatter) Summary(entry *models.LogEntry) string {
	content := ""
	switch v := entry.Content.(type) {
	case string:
		if len(v) > 50 {
			content = v[:50] + "..."
		} else {
			content = v
		}
	case map[string]interface{}:
		if msg, ok := v["message"].(string); ok {
			if len(msg) > 50 {
				content = msg[:50] + "..."
			} else {
				content = msg
			}
		}
	}

	return fmt.Sprintf("%s | %s | %s | %s",
		entry.Timestamp.Format("15:04:05"),
		entry.Source,
		entry.Type,
		content,
	)
}

// Table formats multiple logs as a table
func (f *Formatter) Table(entries []models.LogEntry) string {
	if len(entries) == 0 {
		return "No logs found"
	}

	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("%-20s %-10s %-15s %-10s %s\n",
		"TIMESTAMP", "LEVEL", "SOURCE", "TYPE", "CONTENT"))
	sb.WriteString(strings.Repeat("-", 100) + "\n")

	// Rows
	for _, entry := range entries {
		content := ""
		switch v := entry.Content.(type) {
		case string:
			if len(v) > 40 {
				content = v[:40] + "..."
			} else {
				content = v
			}
		}

		sb.WriteString(fmt.Sprintf("%-20s %-10s %-15s %-10s %s\n",
			entry.Timestamp.Format("2006-01-02 15:04:05"),
			entry.Level,
			entry.Source,
			entry.Type,
			content,
		))
	}

	return sb.String()
}

// Logfy formats logs similar to logfmt
func (f *Formatter) Logfmt(entry *models.LogEntry) string {
	var pairs []string

	pairs = append(pairs, fmt.Sprintf("ts=%s", entry.Timestamp.Format(time.RFC3339)))
	pairs = append(pairs, fmt.Sprintf("level=%s", entry.Level))

	if entry.Source != "" {
		pairs = append(pairs, fmt.Sprintf("source=%s", entry.Source))
	}
	if entry.Agent != "" {
		pairs = append(pairs, fmt.Sprintf("agent=%s", entry.Agent))
	}
	if entry.Type != "" {
		pairs = append(pairs, fmt.Sprintf("type=%s", entry.Type))
	}
	if entry.Model != "" {
		pairs = append(pairs, fmt.Sprintf("model=%s", entry.Model))
	}
	if entry.SessionID != "" {
		pairs = append(pairs, fmt.Sprintf("session=%s", entry.SessionID))
	}

	// Content as message
	switch v := entry.Content.(type) {
	case string:
		pairs = append(pairs, fmt.Sprintf("msg=%q", v))
	case map[string]interface{}:
		if msg, ok := v["message"].(string); ok {
			pairs = append(pairs, fmt.Sprintf("msg=%q", msg))
		}
	}

	if entry.TokensIn > 0 {
		pairs = append(pairs, fmt.Sprintf("tokens_in=%d", entry.TokensIn))
	}
	if entry.TokensOut > 0 {
		pairs = append(pairs, fmt.Sprintf("tokens_out=%d", entry.TokensOut))
	}
	if entry.LatencyMs > 0 {
		pairs = append(pairs, fmt.Sprintf("latency_ms=%d", entry.LatencyMs))
	}
	if entry.Error != "" {
		pairs = append(pairs, fmt.Sprintf("error=%q", entry.Error))
	}

	return strings.Join(pairs, " ")
}
