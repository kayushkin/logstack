package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kayushkin/logstack/models"
)

// Client is the logstack API client
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new logstack client
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Log sends a single log entry
func (c *Client) Log(entry models.LogEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/api/v1/logs",
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return fmt.Errorf("post request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	return nil
}

// LogBatch sends multiple log entries
func (c *Client) LogBatch(entries []models.LogEntry) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal entries: %w", err)
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/api/v1/logs/batch",
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return fmt.Errorf("post request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	return nil
}

// Query retrieves logs matching the given parameters
func (c *Client) Query(params models.QueryParams) ([]models.LogEntry, error) {
	// Build query string
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/logs", nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	if params.Orchestrator != "" {
		q.Add("orchestrator", params.Orchestrator)
	}
	if params.Agent != "" {
		q.Add("agent", params.Agent)
	}
	if params.Channel != "" {
		q.Add("channel", params.Channel)
	}
	if params.SessionID != "" {
		q.Add("session_id", params.SessionID)
	}
	if params.Model != "" {
		q.Add("model", params.Model)
	}
	if params.Level != "" {
		q.Add("level", params.Level)
	}
	if params.Type != "" {
		q.Add("type", params.Type)
	}
	if !params.From.IsZero() {
		q.Add("from", params.From.Format(time.RFC3339))
	}
	if !params.To.IsZero() {
		q.Add("to", params.To.Format(time.RFC3339))
	}
	if params.Limit > 0 {
		q.Add("limit", fmt.Sprintf("%d", params.Limit))
	}
	if params.Offset > 0 {
		q.Add("offset", fmt.Sprintf("%d", params.Offset))
	}

	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Logs []models.LogEntry `json:"logs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Logs, nil
}

// Get retrieves a single log by ID
func (c *Client) Get(id string) (*models.LogEntry, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/v1/logs/" + id)
	if err != nil {
		return nil, fmt.Errorf("get request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("log not found: %s", id)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var entry models.LogEntry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &entry, nil
}

// Stats retrieves log statistics
func (c *Client) Stats(params models.QueryParams) (*models.Stats, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/stats", nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	if params.Orchestrator != "" {
		q.Add("orchestrator", params.Orchestrator)
	}
	if !params.From.IsZero() {
		q.Add("from", params.From.Format(time.RFC3339))
	}
	if !params.To.IsZero() {
		q.Add("to", params.To.Format(time.RFC3339))
	}

	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var stats models.Stats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &stats, nil
}

// Group retrieves logs grouped by a specific field
func (c *Client) Group(params models.QueryParams, groupBy string) ([]models.GroupedLogs, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/logs/group/"+groupBy, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	if params.Orchestrator != "" {
		q.Add("orchestrator", params.Orchestrator)
	}
	if !params.From.IsZero() {
		q.Add("from", params.From.Format(time.RFC3339))
	}
	if !params.To.IsZero() {
		q.Add("to", params.To.Format(time.RFC3339))
	}

	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Groups []models.GroupedLogs `json:"groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Groups, nil
}
