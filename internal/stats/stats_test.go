package stats

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestCounting(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := New()
	r := gin.New()
	r.Use(s.Middleware())
	r.GET("/stats", s.Handler())
	r.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })

	// Make some requests
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/ping", nil)
		r.ServeHTTP(w, req)
	}

	// Check stats
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/stats", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var snap Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 5 pings + 1 stats request = 6
	if snap.TotalRequests != 6 {
		t.Errorf("expected 6 total requests, got %d", snap.TotalRequests)
	}

	if snap.UptimeSeconds <= 0 {
		t.Errorf("expected positive uptime, got %f", snap.UptimeSeconds)
	}

	if snap.StartedAt == "" {
		t.Error("expected non-empty started_at")
	}
}
