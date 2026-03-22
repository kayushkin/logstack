package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kayushkin/bus"
	"github.com/kayushkin/logstack/internal/api"
	"github.com/kayushkin/logstack/internal/stats"
	"github.com/kayushkin/logstack/internal/store"
	"github.com/kayushkin/logstack/models"
)

func main() {
	// Get configuration from environment
	port := getEnv("LOGSTACK_PORT", "8081")
	dataDir := getEnv("LOGSTACK_DATA_DIR", "./logs")
	ginMode := getEnv("GIN_MODE", "release")
	natsURL := getEnv("NATS_URL", "nats://localhost:4222")

	// Set gin mode
	gin.SetMode(ginMode)

	// Initialize store
	s, err := store.NewFileStore(dataDir)
	if err != nil {
		log.Fatalf("Failed to initialize store: %v", err)
	}
	log.Printf("Log stack initialized with data dir: %s", dataDir)

	// Connect to NATS
	nc, err := bus.Connect(bus.Options{URL: natsURL, Name: "logstack"})
	if err != nil {
		log.Printf("WARNING: NATS connection failed: %v (continuing without NATS)", err)
	} else {
		defer nc.Close()
		log.Printf("Connected to NATS at %s", natsURL)
		setupNATS(nc, s)
	}

	// Create API handler
	h := api.NewHandler(s)

	// Server stats (request counting, uptime)
	serverStats := stats.New()

	// Setup router
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())
	r.Use(serverStats.Middleware())

	// Server stats endpoint
	r.GET("/stats", serverStats.Handler())

	// Setup routes
	h.SetupRoutes(r)

	// Start server
	log.Printf("Log stack listening on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func setupNATS(nc *bus.Client, s store.Store) {
	// Subscribe to logs.> for log ingestion
	_, err := nc.Subscribe("logs.>", func(subject string, data []byte) {
		var entry models.LogEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			log.Printf("NATS: failed to unmarshal log entry on %s: %v", subject, err)
			return
		}

		// Set defaults
		if entry.ID == "" {
			entry.ID = uuid.New().String()
		}
		if entry.Timestamp.IsZero() {
			entry.Timestamp = time.Now()
		}
		// Extract orchestrator from subject if not set (e.g. "logs.scheduler" -> "scheduler")
		if entry.Orchestrator == "" {
			parts := strings.SplitN(subject, ".", 2)
			if len(parts) > 1 {
				entry.Orchestrator = parts[1]
			}
		}

		if err := s.Write(&entry); err != nil {
			log.Printf("NATS: failed to write log entry: %v", err)
			return
		}
	})
	if err != nil {
		log.Printf("NATS: failed to subscribe to logs.>: %v", err)
	} else {
		log.Printf("NATS: subscribed to logs.>")
	}

	// Subscribe to chat.stream.* for streaming events (from all orchestrators)
	_, err = nc.Subscribe("chat.stream.*", func(subject string, data []byte) {
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			log.Printf("NATS: failed to unmarshal chat.stream.* on %s: %v", subject, err)
			return
		}
		entry := chatEventToLogEntry(raw, subject)
		if entry == nil {
			return
		}
		if err := s.Write(entry); err != nil {
			log.Printf("NATS: failed to write chat.stream.* entry: %v", err)
		}
	})
	if err != nil {
		log.Printf("NATS: failed to subscribe to chat.stream.*: %v", err)
	} else {
		log.Printf("NATS: subscribed to chat.stream.*")
	}

	// Subscribe to chat.completed for finished messages (JetStream)
	err = nc.JetSubscribe("chat.completed", "logstack", func(subject string, data []byte) {
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			log.Printf("NATS: failed to unmarshal chat.completed: %v", err)
			return
		}
		entry := chatEventToLogEntry(raw, subject)
		if entry == nil {
			return
		}
		// Completed messages are outbound with level=info
		entry.Type = "outbound"
		entry.Level = "info"
		if err := s.Write(entry); err != nil {
			log.Printf("NATS: failed to write chat.completed entry: %v", err)
		}
	})
	if err != nil {
		log.Printf("NATS: failed to subscribe to chat.completed: %v", err)
	} else {
		log.Printf("NATS: subscribed to chat.completed (JetStream)")
	}

	// Reply handler for logstack.query
	_, err = nc.Reply("logstack.query", func(data []byte) (any, error) {
		var params models.QueryParams
		if err := json.Unmarshal(data, &params); err != nil {
			return nil, err
		}
		if params.Limit == 0 {
			params.Limit = 100
		}

		logs, err := s.Query(params)
		if err != nil {
			return nil, err
		}

		return map[string]any{
			"logs":  logs,
			"count": len(logs),
		}, nil
	})
	if err != nil {
		log.Printf("NATS: failed to register logstack.query handler: %v", err)
	} else {
		log.Printf("NATS: registered reply handler for logstack.query")
	}
}

// chatEventToLogEntry converts a ChatOutbound/OutboundMessage from NATS into a LogEntry.
// Returns nil if the event should be skipped (e.g. status events).
func chatEventToLogEntry(raw map[string]interface{}, subject string) *models.LogEntry {
	agent, _ := raw["agent"].(string)
	orchestrator, _ := raw["orchestrator"].(string)
	if agent == "" {
		return nil
	}

	stream, _ := raw["stream"].(string)
	// Skip status events — they're not log-worthy
	if stream == "status" {
		return nil
	}

	// Determine log entry type from stream value
	entryType := "outbound"
	switch stream {
	case "delta", "thinking":
		entryType = stream
	case "tool_call":
		entryType = "tool_call"
	case "tool_result":
		entryType = "tool_result"
	case "done":
		entryType = "outbound"
	}

	text, _ := raw["text"].(string)
	tool, _ := raw["tool"].(string)
	channel, _ := raw["channel"].(string)
	session, _ := raw["session"].(string)
	turnID, _ := raw["turn_id"].(string)

	// Build content matching the shape logEntryToMessage expects
	content := map[string]interface{}{
		"text":         text,
		"agent":        agent,
		"orchestrator": orchestrator,
	}
	if stream != "" {
		content["type"] = stream
	}
	if tool != "" {
		content["tool"] = tool
	}
	if stream == "tool_call" {
		content["tool_input"] = text
	} else if stream == "tool_result" {
		content["tool_output"] = text
	}
	// Copy meta/stats if present
	if meta, ok := raw["meta"]; ok && meta != nil {
		content["stats"] = meta
	}

	var ts time.Time
	if tsRaw, ok := raw["timestamp"]; ok {
		switch v := tsRaw.(type) {
		case string:
			if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
				ts = parsed
			}
		}
	}
	if ts.IsZero() {
		ts = time.Now()
	}

	return &models.LogEntry{
		ID:        uuid.New().String(),
		Timestamp: ts,
		Orchestrator: orchestrator,
		Agent:    agent,
		Channel:  channel,
		SessionID: session,
		TurnID:   turnID,
		Level:    "info",
		Type:     entryType,
		Content:  content,
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
