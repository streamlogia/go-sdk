// Example ardanlabs/service-style Go service integrating the logingestor SDK.
//
// Patterns from github.com/ardanlabs/service by Bill Kennedy:
//   - main() is thin — calls run() and handles the exit code
//   - run() owns all startup/shutdown logic and returns errors
//   - logger is injected (not global) — handlers receive it via closure
//   - HTTP server runs in a goroutine; main goroutine blocks on OS signals
//   - defer client.Close() ensures buffered logs are flushed on shutdown
//
// Logger integration:
//
//	foundation/logger writes JSON to stdout and fires Events callbacks after
//	each record. We use those callbacks to forward every log to Streamlogia —
//	no duplicate code in handlers, no second logger to manage.
//
// To use the real foundation/logger in an ardanlabs/service project, delete
// the local ./logger package and change the import path to:
//
//	"github.com/ardanlabs/service/foundation/logger"
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example/ardanlabs/logger"

	"github.com/streamlogia/go-sdk/logingestor"
)

// =============================================================================
// Config
//
// In the real ardanlabs/service this is loaded via ardanlabs/conf from
// environment variables and command-line flags. Kept minimal here.

type config struct {
	Web struct {
		Host            string
		ShutdownTimeout time.Duration
	}
	Streamlogia struct {
		APIKey    string
		ProjectID string
	}
}

// =============================================================================
// Main / Run

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// ── Config ────────────────────────────────────────────────────────────────
	cfg := config{}
	cfg.Web.Host = envOrDefault("WEB_HOST", ":8080")
	cfg.Web.ShutdownTimeout = 20 * time.Second
	cfg.Streamlogia.APIKey = os.Getenv("LOGINGESTOR_API_KEY")
	cfg.Streamlogia.ProjectID = os.Getenv("LOGINGESTOR_PROJECT_ID")

	// ── Streamlogia client ────────────────────────────────────────────────────
	// Created before the logger so the event callbacks can reference it.
	// defer Close() waits for all in-flight requests to complete before the process exits.
	streamClient := logingestor.New(
		cfg.Streamlogia.APIKey,
		cfg.Streamlogia.ProjectID,
		logingestor.WithSource("sales-api"),
	)
	defer streamClient.Close()

	// ── Logger (foundation/logger style) ─────────────────────────────────────
	// foundation/logger.NewWithEvents wires up two outputs in one call:
	//
	//  1. JSON → stdout  (built internally; captured by systemd/Kubernetes)
	//  2. Events         (our callbacks forward each record to Streamlogia)
	//
	// The Events callbacks receive a Record whose Attributes field contains
	// all structured key-value pairs — it maps directly to logingestor.Entry.Meta.
	//
	// traceIDFn extracts the per-request trace ID from context so it is
	// appended automatically to every log record. In an ardanlabs/service
	// project the trace ID lives in web.Values; adjust the extractor to match
	// how your middleware stores it.
	traceIDFn := func(ctx context.Context) string {
		v, _ := ctx.Value(traceIDKey{}).(string)
		return v
	}

	log := logger.NewWithEvents(
		os.Stdout,
		logger.LevelInfo,
		"sales-api",
		traceIDFn,
		logger.Events{
			Debug: func(ctx context.Context, r logger.Record) {
				streamClient.Debug(ctx, r.Message, r.Attributes)
			},
			Info: func(ctx context.Context, r logger.Record) {
				streamClient.Info(ctx, r.Message, r.Attributes)
			},
			Warn: func(ctx context.Context, r logger.Record) {
				streamClient.Warn(ctx, r.Message, r.Attributes)
			},
			Error: func(ctx context.Context, r logger.Record) {
				streamClient.Error(ctx, r.Message, r.Attributes)
			},
		},
	)

	log.Info(context.Background(), "startup", "service", "sales-api", "status", "initializing")

	// ── HTTP mux ──────────────────────────────────────────────────────────────
	// In the real project routes live in app/services/sales-api/routes/.
	// Handlers are methods on a handler group struct that carries the logger
	// and business-layer dependencies as fields.
	mux := http.NewServeMux()
	mux.Handle("POST /orders", createOrder(log))
	mux.Handle("GET /orders/{id}", getOrder(log))

	// The Streamlogia HTTP middleware automatically logs every request:
	// method, path, status code, duration, remote address. No per-handler
	// HTTP logging needed.
	handler := streamClient.HTTPMiddleware(mux)

	// ── HTTP server ───────────────────────────────────────────────────────────
	api := &http.Server{
		Addr:         cfg.Web.Host,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
		// Route net/http's internal errors through the same structured logger.
		ErrorLog: logger.NewStdLogger(log, logger.LevelError),
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Info(context.Background(), "startup", "status", "api router started", "addr", cfg.Web.Host)
		serverErrors <- api.ListenAndServe()
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		return fmt.Errorf("server error: %w", err)

	case sig := <-shutdown:
		log.Info(context.Background(), "shutdown", "status", "started", "signal", sig)
		defer log.Info(context.Background(), "shutdown", "status", "complete", "signal", sig)

		ctx, cancel := context.WithTimeout(context.Background(), cfg.Web.ShutdownTimeout)
		defer cancel()

		if err := api.Shutdown(ctx); err != nil {
			api.Close()
			return fmt.Errorf("could not stop server gracefully: %w", err)
		}
	}

	return nil
}

// =============================================================================
// Handlers
//
// In the real ardanlabs/service, handlers are methods on a handler group
// struct (e.g. type orderGrp struct { log *logger.Logger; ... }).
// Closures work fine for this self-contained example.

func createOrder(log *logger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Log business events — things the HTTP middleware can't know about.
		// HTTP concerns (method, path, status, duration) are handled by the
		// Streamlogia middleware above; don't duplicate them here.
		log.Info(ctx, "order", "status", "processing")

		// ... parse body, validate, call business layer, persist ...

		w.WriteHeader(http.StatusCreated)
	})
}

func getOrder(log *logger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// ... fetch from business layer ...

		_ = log // log is available for business-level events
		w.WriteHeader(http.StatusOK)
	})
}

// =============================================================================

// traceIDKey is the context key for the per-request trace ID.
// In ardanlabs/service this lives in foundation/web as part of web.Values.
type traceIDKey struct{}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
