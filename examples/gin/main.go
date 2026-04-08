// Example Gin service that uses the logingestor SDK.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
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

	// ── 3. Gin router ────────────────────────────────────────────────────────
	gin.SetMode(gin.ReleaseMode) // disable Gin's built-in logger; ours handles it
	r := gin.New()
	r.Use(streamlogiaMiddleware(client))

	r.POST("/orders", createOrder)
	r.GET("/orders/:id", getOrder)

	slog.Info("server starting", "addr", ":8080")
	if err := r.Run(":8080"); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// streamlogiaMiddleware logs every Gin request to the ingestor.
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

func createOrder(c *gin.Context) {
	c.Status(201)
}

func getOrder(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.String(400, "bad request")
		return
	}
	c.Status(200)
}
