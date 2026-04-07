// Example Go service that uses the logingestor SDK.
package main

import (
	"log/slog"
	"net/http"
	"os"

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
	// Option A: ingestor only — logs go to the ingestor but NOT to stdout.
	// Running `journalctl -u your-service` on the server will show nothing from
	// your app (only systemd start/stop events). Use this only if your team
	// relies solely on the ingestor dashboard for observability.
	// logger := slog.New(logingestor.NewSlogHandler(client))

	// Option B: stdout AND ingestor — logs are written to stdout (captured by
	// systemd and visible via `journalctl -u your-service -f`) AND sent to the
	// ingestor. Recommended for most deployments.
	logger := slog.New(logingestor.MultiHandler{
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}),
		logingestor.NewSlogHandler(client),
	})
	slog.SetDefault(logger)

	// ── 3. HTTP server ───────────────────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", createOrder)
	mux.HandleFunc("GET /orders/{id}", getOrder)

	// Middleware handles all HTTP-level logging automatically — method, path,
	// status code, and duration are captured for every request without any
	// per-handler code. Errors returned by handlers are caught here too.
	handler := client.HTTPMiddleware(mux)

	slog.Info("server starting", "addr", ":8080")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func createOrder(w http.ResponseWriter, r *http.Request) {
	// No logging here — the middleware captures the status code and logs the
	// request automatically. Business logic returns errors; it does not log.
	w.WriteHeader(http.StatusCreated)
}

func getOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}
