package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/kayushkin/bus"
	"github.com/kayushkin/llm-bridge/servicesettings"
	"github.com/kayushkin/logstack/internal/api"
	"github.com/kayushkin/logstack/internal/config"
	"github.com/kayushkin/logstack/internal/stats"
	"github.com/kayushkin/logstack/internal/store"
	"github.com/kayushkin/logstack/models"
)

func main() {
	settings, err := config.NewSettingsRegistry(servicesettings.ProcessEnvironment())
	if err != nil {
		log.Fatalf("read settings: %v", err)
	}
	port := settings.Integer(config.SettingPort)
	dataDir := settings.String(config.SettingDataDirectory)
	ginMode := settings.String(config.SettingGinMode)
	natsURL := settings.String(config.SettingNATSURL)

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
	r.GET("/settings", gin.WrapH(config.SettingsHandler(settings)))

	// Setup routes
	h.SetupRoutes(r)

	// Start server
	log.Printf("Log stack listening on :%d", port)
	if err := r.Run(fmt.Sprintf(":%d", port)); err != nil {
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

	// Subscribe to chat.inbound.* for user messages (all orchestrators via wildcard)
	_, err = nc.Subscribe("chat.inbound.*", func(subject string, data []byte) {
		var msg struct {
			Text         string `json:"text"`
			Author       string `json:"author"`
			Agent        string `json:"agent"`
			Orchestrator string `json:"orchestrator"`
			Channel      string `json:"channel"`
			SessionID    string `json:"session_id"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("NATS: failed to unmarshal chat.inbound: %v", err)
			return
		}
		if msg.Agent == "" || msg.Text == "" {
			return
		}
		// Skip system messages
		if strings.HasPrefix(msg.Text, "[System Message]") || strings.HasPrefix(msg.Text, "System:") {
			return
		}
		author := msg.Author
		if author == "" {
			author = "user"
		}
		sessionID := msg.SessionID
		if sessionID == "" {
			sessionID = "main"
		}
		entry := &models.LogEntry{
			ID:           uuid.New().String(),
			Timestamp:    time.Now(),
			Agent:        msg.Agent,
			Orchestrator: msg.Orchestrator,
			Channel:      msg.Channel,
			SessionID:    sessionID,
			Level:        "info",
			Type:         models.TypeInbound,
			Content: map[string]interface{}{
				"text":   msg.Text,
				"author": author,
			},
		}
		if err := s.Write(entry); err != nil {
			log.Printf("NATS: failed to write chat.inbound entry: %v", err)
		}
	})
	if err != nil {
		log.Printf("NATS: failed to subscribe to chat.inbound: %v", err)
	} else {
		log.Printf("NATS: subscribed to chat.inbound")
	}

	// Subscribe to chat.outbound for completed messages (JetStream, own consumer)
	err = nc.JetSubscribe("chat.outbound", "logstack", func(subject string, data []byte) {
		var msg struct {
			Agent        string           `json:"agent"`
			Orchestrator string           `json:"orchestrator"`
			SessionID    string           `json:"session_id"`
			Text         string           `json:"text"`
			Stats        *models.TurnStats `json:"stats"`
			Timestamp    time.Time        `json:"timestamp"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("NATS: failed to unmarshal chat.outbound: %v", err)
			return
		}
		if msg.Agent == "" || msg.Text == "" {
			return
		}
		content := map[string]interface{}{
			"text":   msg.Text,
			"author": msg.Agent,
		}
		entry := &models.LogEntry{
			ID:           uuid.New().String(),
			Timestamp:    msg.Timestamp,
			Agent:        msg.Agent,
			Orchestrator: msg.Orchestrator,
			SessionID:    msg.SessionID,
			Level:        "info",
			Type:         models.TypeOutbound,
			Content:      content,
			Stats:        msg.Stats,
		}
		if err := s.Write(entry); err != nil {
			log.Printf("NATS: failed to write chat.outbound entry: %v", err)
		}
	})
	if err != nil {
		log.Printf("NATS: failed to subscribe to chat.outbound: %v", err)
	} else {
		log.Printf("NATS: subscribed to chat.outbound (JetStream, consumer: logstack)")
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

