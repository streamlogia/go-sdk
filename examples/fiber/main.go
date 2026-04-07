// Example Fiber service that uses the logingestor SDK.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/streamlogia/go-sdk/logingestor"
)

func main() {
	token := os.Getenv("LOGINGESTOR_TOKEN")
	projectID := os.Getenv("LOGINGESTOR_PROJECT_ID")

	// ── 1. Create the client ─────────────────────────────────────────────────
	client := logingestor.New(
		"https://api.streamlogia.com",
		token,
		projectID,
		logingestor.WithSource("order-service"),
	)
	defer client.Close() // flushes remaining logs on shutdown

	// ── 2. Set up the logger ─────────────────────────────────────────────────
	// Logs go to stdout (journalctl) AND the ingestor dashboard.
	logger := slog.New(logingestor.MultiHandler{
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}),
		logingestor.NewSlogHandler(client),
	})
	slog.SetDefault(logger)

	// ── 3. Fiber app ─────────────────────────────────────────────────────────
	app := fiber.New()

	// Register the middleware before routes so every request is logged.
	app.Use(streamlogiaMiddleware(client))

	app.Post("/orders", createOrder)
	app.Get("/orders/:id", getOrder)

	slog.Info("server starting", "addr", ":8080")
	if err := app.Listen(":8080"); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// streamlogiaMiddleware logs every Fiber request to the ingestor.
func streamlogiaMiddleware(client *logingestor.Client) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		// Call next handler first so we can read the response status afterward.
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

		// *fasthttp.RequestCtx satisfies context.Context.
		ctx := c.Context()
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

func createOrder(c *fiber.Ctx) error {
	// Business logic here. The middleware captures the status and logs it —
	// no per-handler logging needed.
	return c.SendStatus(fiber.StatusCreated)
}

func getOrder(c *fiber.Ctx) error {
	id := c.Params("id")
	if id == "" {
		return c.Status(fiber.StatusBadRequest).SendString("bad request")
	}
	return c.SendStatus(fiber.StatusOK)
}
