package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
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

// streamJSON writes obj to the response as it encodes it.
//
// Use it for the responses that carry log rows; c.JSON is fine for the small
// fixed-shape ones. gin's c.JSON marshals the ENTIRE response into one []byte
// before writing a single byte of it, and json.Marshal grows that buffer by
// doubling and then copies it — so serialising the 30-day group response cost
// ~173MB of transient heap on top of the ~90MB of entries it was serialising, to
// send a body the client streams anyway. Measured on the live corpus; it was a
// quarter of the 710MB peak.
//
// Encoding straight into the ResponseWriter also means a client that gives up
// mid-download stops the work, instead of the server building all 90MB first and
// discovering nobody is listening.
//
// The bytes are identical to c.JSON's apart from encoding/json's trailing
// newline, which JSON treats as insignificant whitespace.
func streamJSON(c *gin.Context, status int, obj any) {
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(status)

	// The status line is already committed, so a mid-stream failure cannot become
	// a clean 500 — the client sees a truncated body and a broken parse either
	// way. It must not pass silently, though: log it. In practice Content is
	// whatever json.Unmarshal produced on the way in, so it always re-marshals;
	// the realistic error here is the client hanging up.
	if err := json.NewEncoder(c.Writer).Encode(obj); err != nil {
		log.Printf("stream json response: %v", err)
		c.Error(err) //nolint:errcheck // recorded on the context for gin's logger
	}
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
		api.GET("/max-usage", h.GetMaxUsage)
		api.GET("/rate-limits", h.GetRateLimits)

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

	streamJSON(c, http.StatusOK, gin.H{
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

	if !store.IsGroupField(groupBy) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid group field",
			"valid": store.GroupFields,
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

	streamJSON(c, http.StatusOK, gin.H{
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

// GetMaxUsage handles GET /api/v1/max-usage
func (h *Handler) GetMaxUsage(c *gin.Context) {
	now := time.Now().UTC()

	// Calculate billing period (1st of current month to 1st of next month)
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	usage, err := h.store.MaxUsage(periodStart, periodEnd)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, usage)
}

// GetRateLimits handles GET /api/v1/rate-limits
func (h *Handler) GetRateLimits(c *gin.Context) {
	now := time.Now().UTC()

	// Default to last 7 days
	from := now.Add(-7 * 24 * time.Hour)
	limit := 100

	// Allow custom from parameter
	if fromStr := c.Query("from"); fromStr != "" {
		if parsed, err := time.Parse(time.RFC3339, fromStr); err == nil {
			from = parsed
		}
	}

	// Allow custom limit
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	rateLimits, err := h.store.RateLimits(from, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, rateLimits)
}

// Health handles GET /api/v1/health
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
	})
}
