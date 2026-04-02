package models

import "time"

// LogEntry represents a single log entry in the standard format.
type LogEntry struct {
	// Unique identifier for this log entry
	ID string `json:"id"`

	// Timestamp when the log was created (ISO 8601)
	Timestamp time.Time `json:"timestamp"`

	// Orchestrator that ran this turn (openclaw, inber)
	Orchestrator string `json:"orchestrator"`

	// Agent/instance identifier
	Agent string `json:"agent,omitempty"`

	// Channel where the interaction occurred (discord, tui, websocket)
	Channel string `json:"channel,omitempty"`

	// Session ID for grouping related logs
	SessionID string `json:"session_id,omitempty"`

	// Turn ID — unique within a session (inber: turn number, openclaw: message id)
	TurnID string `json:"turn_id,omitempty"`

	// Model used (opus46, glm-5, sonnet-4-5, etc.)
	Model string `json:"model,omitempty"`

	// Log level: debug, info, warn, error
	Level string `json:"level"`

	// Type of log entry: inbound, outbound, error, metrics, lifecycle, routing
	Type string `json:"type"`

	// The actual content/payload (text, author, thinking)
	Content interface{} `json:"content"`

	// Turn stats for outbound messages (tokens, cost, tools)
	Stats *TurnStats `json:"stats,omitempty"`

	// Token usage (deprecated — use Stats)
	TokensIn  int `json:"tokens_in,omitempty"`
	TokensOut int `json:"tokens_out,omitempty"`

	// Latency in milliseconds (deprecated — use Stats.DurationMs)
	LatencyMs int64 `json:"latency_ms,omitempty"`

	// Hidden entries are still logged but should not be displayed in frontends.
	// Set by the adapter for system messages, heartbeat acks, NO_REPLY, etc.
	Hidden bool `json:"hidden,omitempty"`

	// Error message (if level is error)
	Error string `json:"error,omitempty"`

	// Additional metadata (deprecated — use Stats)
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// TurnStats holds structured token/cost/tool stats for an outbound message.
type TurnStats struct {
	InputTokens         int         `json:"input_tokens,omitempty"`
	OutputTokens        int         `json:"output_tokens,omitempty"`
	CacheReadTokens     int         `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int         `json:"cache_creation_tokens,omitempty"`
	Cost                float64     `json:"cost,omitempty"`
	DurationMs          int64       `json:"duration_ms,omitempty"`
	Model               string      `json:"model,omitempty"`
	ToolCalls           int         `json:"tool_calls,omitempty"`
	Tools               []ToolEvent `json:"tools,omitempty"`
}

// ToolEvent represents a single tool invocation within a turn.
type ToolEvent struct {
	Tool       string `json:"tool"`
	ToolInput  string `json:"tool_input,omitempty"`
	ToolOutput string `json:"tool_output,omitempty"`
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
	Orchestrator string `form:"orchestrator"`
	Agent     string    `form:"agent"`
	Channel   string    `form:"channel"`
	SessionID string    `form:"session_id"`
	TurnID    string    `form:"turn_id"`
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
	ByOrch         map[string]int `json:"by_orchestrator"`
	ByLevel        map[string]int `json:"by_level"`
	ByModel        map[string]int `json:"by_model"`
	TotalTokensIn  int            `json:"total_tokens_in"`
	TotalTokensOut int            `json:"total_tokens_out"`
	AvgLatencyMs   float64        `json:"avg_latency_ms"`
}

// MaxUsageTotals holds overall usage totals for the billing period
type MaxUsageTotals struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	APICalls         int     `json:"api_calls"`
	EstimatedCost    float64 `json:"estimated_cost"`
}

// MaxUsageByModel holds usage stats for a single model
type MaxUsageByModel struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	APICalls         int     `json:"api_calls"`
	EstimatedCost    float64 `json:"estimated_cost"`
}

// MaxUsageByOrchestrator holds usage stats for a single orchestrator
type MaxUsageByOrchestrator struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	APICalls         int     `json:"api_calls"`
	EstimatedCost    float64 `json:"estimated_cost"`
}

// MaxUsageByDay holds usage stats for a single day
type MaxUsageByDay struct {
	Date             string  `json:"date"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	APICalls         int     `json:"api_calls"`
	Cost             float64 `json:"cost"`
}

// MaxUsageRateLimits holds rate limit information
type MaxUsageRateLimits struct {
	Count429         int    `json:"429_count"`
	Last429          string `json:"last_429,omitempty"`
	ThrottledMinutes int    `json:"throttled_minutes"`
}

// MaxUsageResponse is the comprehensive API response for /api/v1/max-usage
type MaxUsageResponse struct {
	PeriodStart    string                          `json:"period_start"`
	PeriodEnd      string                          `json:"period_end"`
	Totals         MaxUsageTotals                  `json:"totals"`
	ByModel        map[string]MaxUsageByModel      `json:"by_model"`
	ByOrchestrator map[string]MaxUsageByOrchestrator `json:"by_orchestrator"`
	ByDay          []MaxUsageByDay                 `json:"by_day"`
	RateLimits     MaxUsageRateLimits              `json:"rate_limits"`
}

// RateLimitEvent represents a single 429 error event
type RateLimitEvent struct {
	Timestamp   string `json:"timestamp"`
	Model       string `json:"model,omitempty"`
	Orchestrator string `json:"orchestrator,omitempty"`
	Message     string `json:"message,omitempty"`
}

// RateLimitsResponse is the API response for /api/v1/rate-limits
type RateLimitsResponse struct {
	Total  int              `json:"total"`
	Events []RateLimitEvent `json:"events"`
}
