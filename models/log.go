package models

import "time"

// LogEntry represents a single log entry in the standard format.
type LogEntry struct {
	// Unique identifier for this log entry
	ID string `json:"id"`

	// Timestamp when the log was created (ISO 8601)
	Timestamp time.Time `json:"timestamp"`

	// Source service (inber, si, claxon-android, etc.)
	Source string `json:"source"`

	// Agent/instance identifier (task-manager, worker, etc.)
	Agent string `json:"agent,omitempty"`

	// Channel where the interaction occurred (discord, tui, websocket)
	Channel string `json:"channel,omitempty"`

	// Session ID for grouping related logs
	SessionID string `json:"session_id,omitempty"`

	// Model used (opus46, glm-5, sonnet-4-5, etc.)
	Model string `json:"model,omitempty"`

	// Log level: debug, info, warn, error
	Level string `json:"level"`

	// Type of log entry (message, tool_call, error, metrics, etc.)
	Type string `json:"type"`

	// The actual content/payload
	Content interface{} `json:"content"`

	// Token usage (if applicable)
	TokensIn  int `json:"tokens_in,omitempty"`
	TokensOut int `json:"tokens_out,omitempty"`

	// Latency in milliseconds (if applicable)
	LatencyMs int64 `json:"latency_ms,omitempty"`

	// Error message (if level is error)
	Error string `json:"error,omitempty"`

	// Additional metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// LogType constants
const (
	TypeMessage   = "message"
	TypeToolCall  = "tool_call"
	TypeToolResult = "tool_result"
	TypeError     = "error"
	TypeMetrics   = "metrics"
	TypeLifecycle = "lifecycle"
	TypeRouting   = "routing"
)

// Level constants
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// QueryParams for searching logs
type QueryParams struct {
	Source    string    `form:"source"`
	Agent     string    `form:"agent"`
	Channel   string    `form:"channel"`
	SessionID string    `form:"session_id"`
	Model     string    `form:"model"`
	Level     string    `form:"level"`
	Type      string    `form:"type"`
	From      time.Time `form:"from" time_format:"2006-01-02T15:04:05Z07:00"`
	To        time.Time `form:"to" time_format:"2006-01-02T15:04:05Z07:00"`
	Limit     int       `form:"limit"`
	Offset    int       `form:"offset"`
}

// GroupedLogs for aggregated views
type GroupedLogs struct {
	GroupKey string     `json:"group_key"`
	Count    int        `json:"count"`
	Logs     []LogEntry `json:"logs,omitempty"`
}

// UsageStats holds aggregated token usage for an agent over a time period.
type UsageStats struct {
	Agent        string `json:"agent"`
	Orchestrator string `json:"orchestrator"`
	Model        string `json:"model,omitempty"`
	Messages     int    `json:"messages"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
	DurationMs   int64  `json:"duration_ms"`
}

// UsageResponse is the API response for /api/v1/usage.
type UsageResponse struct {
	Day   []UsageStats `json:"day"`
	Week  []UsageStats `json:"week"`
	Month []UsageStats `json:"month"`
}

// Stats for log statistics
type Stats struct {
	TotalEntries   int            `json:"total_entries"`
	BySource       map[string]int `json:"by_source"`
	ByLevel        map[string]int `json:"by_level"`
	ByModel        map[string]int `json:"by_model"`
	TotalTokensIn  int            `json:"total_tokens_in"`
	TotalTokensOut int            `json:"total_tokens_out"`
	AvgLatencyMs   float64        `json:"avg_latency_ms"`
}
