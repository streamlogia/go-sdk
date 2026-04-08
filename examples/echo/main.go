// Example Echo service that uses the logingestor SDK.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/streamlogia/go-sdk/logingestor"
)

func main() {
	apiKey := os.Getenv("LOGINGESTOR_API_KEY")
	projectID := os.Getenv("LOGINGESTOR_PROJECT_ID")

	// ── 1. Create the client ─────────────────────────────────────────────────
	client := logingestor.New(
		apiKey,
		projectID,
		logingestor.WithSource("order-service"),
	)
	defer client.Close()

	// ── 2. Set up the logger ─────────────────────────────────────────────────
	logger := slog.New(logingestor.MultiHandler{
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}),
		logingestor.NewSlogHandler(client),
	})
	slog.SetDefault(logger)

	// ── 3. Echo router ───────────────────────────────────────────────────────
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(streamlogiaMiddleware(client))

	e.POST("/orders", createOrder)
	e.GET("/orders/:id", getOrder)

	slog.Info("server starting", "addr", ":8080")
	if err := e.Start(":8080"); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// streamlogiaMiddleware logs every Echo request to the ingestor.
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

func createOrder(c echo.Context) error {
	return c.NoContent(201)
}

func getOrder(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.String(400, "bad request")
	}
	return c.NoContent(200)
}
