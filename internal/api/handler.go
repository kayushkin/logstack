package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kayushkin/logstack/models"
	"github.com/kayushkin/logstack/internal/store"
)

// Handler holds API handlers
type Handler struct {
	store store.Store
}

// NewHandler creates a new API handler
func NewHandler(s store.Store) *Handler {
	return &Handler{store: s}
}

// SetupRoutes configures all API routes
func (h *Handler) SetupRoutes(r *gin.Engine) {
	api := r.Group("/api/v1")
	{
		// Log ingestion
		api.POST("/logs", h.IngestLog)
		api.POST("/logs/batch", h.IngestBatch)

		// Querying
		api.GET("/logs", h.QueryLogs)
		api.GET("/logs/:id", h.GetLog)

		// Aggregation
		api.GET("/logs/group/:field", h.GroupLogs)
		api.GET("/stats", h.GetStats)

		// Usage aggregation
		api.GET("/usage", h.GetUsage)

		// Health
		api.GET("/health", h.Health)
	}
}

// IngestLog handles POST /api/v1/logs
func (h *Handler) IngestLog(c *gin.Context) {
	var entry models.LogEntry
	if err := c.ShouldBindJSON(&entry); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.store.Write(&entry); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":      entry.ID,
		"status":  "created",
		"message": "Log entry created successfully",
	})
}

// IngestBatch handles POST /api/v1/logs/batch
func (h *Handler) IngestBatch(c *gin.Context) {
	var entries []models.LogEntry
	if err := c.ShouldBindJSON(&entries); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var created, failed int
	for i := range entries {
		if err := h.store.Write(&entries[i]); err != nil {
			failed++
		} else {
			created++
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"created": created,
		"failed":  failed,
		"status":  "batch processed",
	})
}

// QueryLogs handles GET /api/v1/logs
func (h *Handler) QueryLogs(c *gin.Context) {
	var params models.QueryParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Set defaults
	if params.Limit == 0 {
		params.Limit = 100
	}

	logs, err := h.store.Query(params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":  logs,
		"count": len(logs),
	})
}

// GetLog handles GET /api/v1/logs/:id
func (h *Handler) GetLog(c *gin.Context) {
	id := c.Param("id")

	log, err := h.store.Get(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, log)
}

// GroupLogs handles GET /api/v1/logs/group/:field
func (h *Handler) GroupLogs(c *gin.Context) {
	groupBy := c.Param("field")

	// Valid group fields
	validFields := map[string]bool{
		"source": true, "agent": true, "channel": true,
		"model": true, "level": true, "type": true,
		"session": true, "hour": true, "day": true,
	}

	if !validFields[groupBy] {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid group field",
			"valid": []string{"source", "agent", "channel", "model", "level", "type", "session", "hour", "day"},
		})
		return
	}

	var params models.QueryParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	groups, err := h.store.Group(params, groupBy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"group_by": groupBy,
		"groups":   groups,
		"count":    len(groups),
	})
}

// GetStats handles GET /api/v1/stats
func (h *Handler) GetStats(c *gin.Context) {
	var params models.QueryParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	stats, err := h.store.Stats(params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, stats)
}

// GetUsage handles GET /api/v1/usage
func (h *Handler) GetUsage(c *gin.Context) {
	now := time.Now().UTC()

	day, err := h.store.Usage(now.Add(-24 * time.Hour))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	week, err := h.store.Usage(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	month, err := h.store.Usage(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Ensure non-nil slices for JSON
	if day == nil {
		day = []models.UsageStats{}
	}
	if week == nil {
		week = []models.UsageStats{}
	}
	if month == nil {
		month = []models.UsageStats{}
	}

	c.JSON(http.StatusOK, models.UsageResponse{
		Day:   day,
		Week:  week,
		Month: month,
	})
}

// Health handles GET /api/v1/health
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
	})
}
