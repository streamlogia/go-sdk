// Package logingestor provides a Go client for the Log Ingestor API.
//
// Every log call sends its entry immediately in a background goroutine.
// Call Close() before your process exits to wait for all in-flight requests.
//
// Basic usage:
//
//	client := logingestor.New("<api-key>", "<project-id>",
//	    logingestor.WithSource("my-service"),
//	)
//	defer client.Close()
//
//	client.Info(ctx, "user signed in", map[string]any{"userId": "u_123"})
package logingestor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Level represents a log severity level.
type Level string

const (
	LevelDebug Level = "DEBUG"
	LevelInfo  Level = "INFO"
	LevelWarn  Level = "WARN"
	LevelError Level = "ERROR"
)

// Entry is a single log record sent to the ingestor.
type Entry struct {
	ProjectID string         `json:"projectId"`
	Level     Level          `json:"level"`
	Message   string         `json:"message"`
	Source    string         `json:"source"`
	Timestamp *time.Time     `json:"timestamp,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// IngestResponse is returned by the /v1/ingest endpoint.
type IngestResponse struct {
	Ingested int      `json:"ingested"`
	IDs      []string `json:"ids"`
}

const baseURL = "https://api.streamlogia.com"

// Client sends log entries to the Log Ingestor service.
// Each log call dispatches a goroutine immediately — there is no internal
// queue or flush interval. Close() waits for all in-flight requests to finish.
type Client struct {
	apiKey     string
	projectID  string
	source     string
	httpClient *http.Client
	wg         sync.WaitGroup
}

// Option configures a Client.
type Option func(*Client)

// WithSource sets the default source field on every log entry.
func WithSource(source string) Option {
	return func(c *Client) { c.source = source }
}

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// New creates a client. Each log method sends its entry immediately.
//
//   - apiKey    – API key obtained from the Streamlogia dashboard
//   - projectID – UUID of the project to ingest into
func New(apiKey, projectID string, opts ...Option) *Client {
	c := &Client{
		apiKey:     apiKey,
		projectID:  projectID,
		source:     "unknown",
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Debug logs at DEBUG level.
func (c *Client) Debug(ctx context.Context, message string, meta map[string]any, tags ...string) {
	c.send(LevelDebug, message, meta, tags)
}

// Info logs at INFO level.
func (c *Client) Info(ctx context.Context, message string, meta map[string]any, tags ...string) {
	c.send(LevelInfo, message, meta, tags)
}

// Warn logs at WARN level.
func (c *Client) Warn(ctx context.Context, message string, meta map[string]any, tags ...string) {
	c.send(LevelWarn, message, meta, tags)
}

// Error logs at ERROR level.
func (c *Client) Error(ctx context.Context, message string, meta map[string]any, tags ...string) {
	c.send(LevelError, message, meta, tags)
}

// Ingest sends entries to the API directly. The call blocks until the HTTP
// request completes. Use this when you need synchronous delivery guarantees.
func (c *Client) Ingest(ctx context.Context, entries []Entry) (*IngestResponse, error) {
	body, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("logingestor: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("logingestor: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("logingestor: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("logingestor: server returned %d: %s", resp.StatusCode, b)
	}

	var result IngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("logingestor: decode response: %w", err)
	}
	return &result, nil
}

// Close waits for all in-flight log requests to complete. Call it (or defer
// it) before your process exits to ensure no entries are dropped.
func (c *Client) Close() error {
	c.wg.Wait()
	return nil
}

// send dispatches a single entry to the API in a background goroutine.
func (c *Client) send(level Level, message string, meta map[string]any, tags []string) {
	now := time.Now().UTC()
	entry := Entry{
		ProjectID: c.projectID,
		Level:     level,
		Message:   message,
		Source:    c.source,
		Timestamp: &now,
		Tags:      tags,
		Meta:      meta,
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		_, _ = c.Ingest(context.Background(), []Entry{entry})
	}()
}
