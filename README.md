# Streamlogia Go SDK

A Go client library for sending structured logs to the [Streamlogia](https://streamlogia.com) Log Ingestor API.

## Features

- Structured log ingestion (DEBUG, INFO, WARN, ERROR)
- Asynchronous batching with configurable flush interval and batch size
- `log/slog` handler integration
- HTTP middleware for automatic request/response logging
- Multi-handler fan-out (e.g. stdout + ingestor simultaneously)
- Zero external dependencies — standard library only

## Requirements

Go 1.21 or later (uses `log/slog`).

## Installation

```sh
go get github.com/streamlogia/go-sdk
```

## Quick Start

```go
package main

import (
    "context"

    "github.com/streamlogia/go-sdk/logingestor"
)

func main() {
    client := logingestor.New(
        "https://api.streamlogia.com",
        "<jwt-token>",
        "<project-id>",
        logingestor.WithSource("my-service"),
    )
    defer client.Close()

    ctx := context.Background()
    client.Info(ctx, "application started", nil)
    client.Info(ctx, "user signed in", map[string]any{"userId": "u_123"}, "auth", "web")
}
```

## Usage

### Direct Client Logging

The `Client` exposes convenience methods for each log level:

```go
client.Debug(ctx, "cache miss", map[string]any{"key": "session:42"})
client.Info(ctx, "order placed", map[string]any{"orderId": "ord_99", "total": 49.95}, "orders")
client.Warn(ctx, "rate limit approaching", map[string]any{"remaining": 5})
client.Error(ctx, "payment failed", map[string]any{"error": err.Error()}, "payments")
```

Each method accepts:

- `ctx context.Context`
- `message string`
- `meta map[string]any` — optional key/value metadata (pass `nil` if unused)
- `tags ...string` — optional tags for filtering

### slog Integration

Wrap the client as a `slog.Handler` to use the standard library logging API:

```go
import "log/slog"

logger := slog.New(logingestor.NewSlogHandler(client))
slog.SetDefault(logger)

slog.Info("payment processed", "amount", 99.99, "currency", "USD")
slog.Error("db query failed", "table", "orders", "error", err)
```

### Dual Output (stdout + ingestor) Recommended approach

Use `MultiHandler` to route logs to multiple destinations simultaneously:

```go
logger := slog.New(logingestor.MultiHandler{
    slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}),
    logingestor.NewSlogHandler(client),
})
slog.SetDefault(logger)
```

### HTTP Middleware

Wrap any `http.Handler` to automatically log every request:

```go
mux := http.NewServeMux()
mux.HandleFunc("/api/orders", ordersHandler)

http.ListenAndServe(":8080", client.HTTPMiddleware(mux))
```

Each request logs method, path, status code, duration, remote address, user agent, and the `X-Request-Id` header (if present). The log level is determined by the response status code:

| Status range | Level |
| ------------ | ----- |
| 2xx / 3xx    | INFO  |
| 4xx          | WARN  |
| 5xx          | ERROR |

### Fiber

Fiber uses [fasthttp](https://github.com/valyala/fasthttp) instead of `net/http`, so `client.HTTPMiddleware` is not compatible. Write a small middleware that calls the client directly — `*fasthttp.RequestCtx` implements `context.Context`, so it passes straight through.

```go
func streamlogiaMiddleware(client *logingestor.Client) fiber.Handler {
    return func(c *fiber.Ctx) error {
        start := time.Now()
        err := c.Next()

        status := c.Response().StatusCode()
        meta := map[string]any{
            "method":     c.Method(),
            "path":       c.Path(),
            "status":     status,
            "durationMs": time.Since(start).Milliseconds(),
            "remoteAddr": c.IP(),
            "userAgent":  c.Get(fiber.HeaderUserAgent),
        }
        if rid := c.Get("X-Request-Id"); rid != "" {
            meta["requestId"] = rid
        }

        msg := fmt.Sprintf("%s %s %d", c.Method(), c.Path(), status)
        ctx := c.Context() // *fasthttp.RequestCtx satisfies context.Context
        switch {
        case status >= 500:
            client.Error(ctx, msg, meta)
        case status >= 400:
            client.Warn(ctx, msg, meta)
        default:
            client.Info(ctx, msg, meta)
        }
        return err
    }
}
```

See [`examples/fiber/main.go`](examples/fiber/main.go) for the full runnable version.

### Gin

```go
func streamlogiaMiddleware(client *logingestor.Client) gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        c.Next()

        status := c.Writer.Status()
        meta := map[string]any{
            "method":     c.Request.Method,
            "path":       c.Request.URL.Path,
            "status":     status,
            "durationMs": time.Since(start).Milliseconds(),
            "remoteAddr": c.ClientIP(),
            "userAgent":  c.Request.UserAgent(),
        }
        if rid := c.GetHeader("X-Request-Id"); rid != "" {
            meta["requestId"] = rid
        }

        msg := fmt.Sprintf("%s %s %d", c.Request.Method, c.Request.URL.Path, status)
        ctx := c.Request.Context()
        switch {
        case status >= 500:
            client.Error(ctx, msg, meta)
        case status >= 400:
            client.Warn(ctx, msg, meta)
        default:
            client.Info(ctx, msg, meta)
        }
    }
}
```

Register it with `gin.New()` (not `gin.Default()`) to avoid duplicate logging from Gin's built-in logger:

```go
r := gin.New()
r.Use(streamlogiaMiddleware(client))
```

See [`examples/gin/main.go`](examples/gin/main.go) for the full runnable version.

### Echo

```go
func streamlogiaMiddleware(client *logingestor.Client) echo.MiddlewareFunc {
    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return func(c echo.Context) error {
            start := time.Now()
            err := next(c)

            status := c.Response().Status
            req := c.Request()
            meta := map[string]any{
                "method":     req.Method,
                "path":       req.URL.Path,
                "status":     status,
                "durationMs": time.Since(start).Milliseconds(),
                "remoteAddr": c.RealIP(),
                "userAgent":  req.UserAgent(),
            }
            if rid := req.Header.Get("X-Request-Id"); rid != "" {
                meta["requestId"] = rid
            }

            msg := fmt.Sprintf("%s %s %d", req.Method, req.URL.Path, status)
            ctx := req.Context()
            switch {
            case status >= 500:
                client.Error(ctx, msg, meta)
            case status >= 400:
                client.Warn(ctx, msg, meta)
            default:
                client.Info(ctx, msg, meta)
            }
            return err
        }
    }
}
```

See [`examples/echo/main.go`](examples/echo/main.go) for the full runnable version.

## Examples

Each example is a self-contained module under `examples/`. Run any of them with:

```sh
cd examples/stdlib   # or fiber, gin, echo
LOGINGESTOR_TOKEN=<token> LOGINGESTOR_PROJECT_ID=<id> go run .
```

## Configuration

Pass option functions to `logingestor.New`:

```go
client := logingestor.New(
    baseURL,
    token,
    projectID,
    logingestor.WithSource("order-service"),
    logingestor.WithBatchSize(50),
    logingestor.WithFlushInterval(10*time.Second),
    logingestor.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
)
```

| Option                               | Default        | Description                                        |
| ------------------------------------ | -------------- | -------------------------------------------------- |
| `WithSource(s string)`               | `""`           | Default `source` field on every log entry          |
| `WithBatchSize(n int)`               | `1`            | Flush automatically after accumulating `n` entries |
| `WithFlushInterval(d time.Duration)` | `5s`           | Background flush interval                          |
| `WithHTTPClient(hc *http.Client)`    | default client | Custom HTTP client (timeouts, transport, etc.)     |

## Graceful Shutdown

Call `client.Close()` (or `defer client.Close()`) before your process exits. It flushes any buffered entries and waits for the background goroutine to stop.

```go
client := logingestor.New(...)
defer client.Close()
```

## API Reference

### Types

```go
type Entry struct {
    ProjectID string
    Level     Level
    Message   string
    Source    string
    Timestamp *time.Time
    Tags      []string
    Meta      map[string]any
}

type IngestResponse struct {
    Ingested int
    IDs      []string
}
```

### Log Levels

```go
logingestor.LevelDebug // "DEBUG"
logingestor.LevelInfo  // "INFO"
logingestor.LevelWarn  // "WARN"
logingestor.LevelError // "ERROR"
```

### Client Methods

| Method                                           | Description                                   |
| ------------------------------------------------ | --------------------------------------------- |
| `New(baseURL, token, projectID, opts...)`        | Create a new client                           |
| `Debug/Info/Warn/Error(ctx, msg, meta, tags...)` | Log at the given level                        |
| `Ingest(ctx, entries)`                           | Send entries immediately, bypassing the queue |
| `Flush(ctx)`                                     | Drain the internal queue                      |
| `Close()`                                        | Flush and stop the background goroutine       |
| `HTTPMiddleware(next http.Handler)`              | HTTP middleware for automatic request logging |

## License

[MIT](LICENSE)
