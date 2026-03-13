package stats

import (
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Server tracks request counts and uptime for the logstack server.
type Server struct {
	totalRequests atomic.Int64
	startedAt     time.Time
}

// New creates a new Server stats tracker.
func New() *Server {
	return &Server{
		startedAt: time.Now(),
	}
}

// Middleware returns a gin middleware that increments the request counter.
func (s *Server) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		s.totalRequests.Add(1)
		c.Next()
	}
}

// Snapshot holds a point-in-time view of server stats.
type Snapshot struct {
	TotalRequests int64   `json:"total_requests"`
	Uptime        string  `json:"uptime"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	StartedAt     string  `json:"started_at"`
}

// Snapshot returns current server stats.
func (s *Server) Snapshot() Snapshot {
	uptime := time.Since(s.startedAt)
	return Snapshot{
		TotalRequests: s.totalRequests.Load(),
		Uptime:        uptime.Round(time.Second).String(),
		UptimeSeconds: uptime.Seconds(),
		StartedAt:     s.startedAt.UTC().Format(time.RFC3339),
	}
}

// Handler returns a gin handler for GET /stats.
func (s *Server) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(200, s.Snapshot())
	}
}
