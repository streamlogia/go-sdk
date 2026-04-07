// Package logingestor provides a Go client for the Log Ingestor API.
//
// Basic usage:
//
//	client := logingestor.New("https://logs.example.com", "<jwt-token>", "<project-id>",
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

// Client sends log entries to the Log Ingestor service.
type Client struct {
	baseURL    string
	token      string
	projectID  string
	source     string
	httpClient *http.Client

	// batching
	mu            sync.Mutex
	queue         []Entry
	batchSize     int
	flushInterval time.Duration
	stopCh        chan struct{}
	wg            sync.WaitGroup
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

// WithBatchSize overrides how many entries to accumulate before flushing.
// Default is 1 (every entry is sent immediately). Increase only if you
// explicitly need to reduce network calls at the cost of latency.
func WithBatchSize(n int) Option {
	return func(c *Client) { c.batchSize = n }
}

// WithFlushInterval sets how often the background goroutine flushes the
// pending queue regardless of batch size. Default is 5 seconds.
func WithFlushInterval(d time.Duration) Option {
	return func(c *Client) { c.flushInterval = d }
}

// New creates a client and starts the background flush goroutine.
//
//   - baseURL  – e.g. "https://logs.example.com" (no trailing slash)
//   - token    – JWT bearer token obtained from the auth service
//   - projectID – UUID of the project to ingest into
func New(baseURL, token, projectID string, opts ...Option) *Client {
	c := &Client{
		baseURL:       baseURL,
		token:         token,
		projectID:     projectID,
		source:        "unknown",
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		batchSize:     1,
		flushInterval: 5 * time.Second,
		stopCh:        make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}

	c.wg.Add(1)
	go c.backgroundFlusher()

	return c
}

// Debug logs at DEBUG level.
func (c *Client) Debug(ctx context.Context, message string, meta map[string]any, tags ...string) {
	c.enqueue(ctx, LevelDebug, message, meta, tags)
}

// Info logs at INFO level.
func (c *Client) Info(ctx context.Context, message string, meta map[string]any, tags ...string) {
	c.enqueue(ctx, LevelInfo, message, meta, tags)
}

// Warn logs at WARN level.
func (c *Client) Warn(ctx context.Context, message string, meta map[string]any, tags ...string) {
	c.enqueue(ctx, LevelWarn, message, meta, tags)
}

// Error logs at ERROR level.
func (c *Client) Error(ctx context.Context, message string, meta map[string]any, tags ...string) {
	c.enqueue(ctx, LevelError, message, meta, tags)
}

// Ingest sends a batch of entries immediately, bypassing the internal queue.
// Useful when you need a synchronous guarantee (e.g., before process exit).
func (c *Client) Ingest(ctx context.Context, entries []Entry) (*IngestResponse, error) {
	body, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("logingestor: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("logingestor: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
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

// Flush drains the internal queue immediately. Call this before your process
// exits to ensure no buffered logs are lost.
func (c *Client) Flush(ctx context.Context) error {
	c.mu.Lock()
	batch := c.queue
	c.queue = nil
	c.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}
	_, err := c.Ingest(ctx, batch)
	return err
}

// Close flushes pending logs and stops the background goroutine.
func (c *Client) Close() error {
	close(c.stopCh)
	c.wg.Wait()
	return c.Flush(context.Background())
}

// enqueue adds an entry to the internal queue and triggers a flush if full.
func (c *Client) enqueue(_ context.Context, level Level, message string, meta map[string]any, tags []string) {
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

	c.mu.Lock()
	c.queue = append(c.queue, entry)
	shouldFlush := len(c.queue) >= c.batchSize
	c.mu.Unlock()

	if shouldFlush {
		go func() {
			_ = c.Flush(context.Background())
		}()
	}
}

func (c *Client) backgroundFlusher() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = c.Flush(context.Background())
		case <-c.stopCh:
			return
		}
	}
}
