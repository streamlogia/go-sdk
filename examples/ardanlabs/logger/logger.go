// Package logger mirrors the API of github.com/ardanlabs/service/foundation/logger.
//
// In a real ardanlabs/service project, delete this package and change the import
// in main.go to:
//
//	"github.com/ardanlabs/service/foundation/logger"
//
// The source below is a faithful copy of the foundation/logger implementation
// so the example compiles without pulling in the full ardanlabs/service module.
package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"path/filepath"
	"runtime"
	"time"
)

// TraceIDFn is a function that extracts a trace ID from the given context.
type TraceIDFn func(ctx context.Context) string

// Level represents a logging level.
type Level slog.Level

// Available log levels.
const (
	LevelDebug = Level(slog.LevelDebug)
	LevelInfo  = Level(slog.LevelInfo)
	LevelWarn  = Level(slog.LevelWarn)
	LevelError = Level(slog.LevelError)
)

// Record is the data delivered to an EventFn callback.
type Record struct {
	Time       time.Time
	Message    string
	Level      Level
	Attributes map[string]any
}

// EventFn is called after a log record is handled by the main handler.
type EventFn func(ctx context.Context, r Record)

// Events maps an EventFn to each log level.
type Events struct {
	Debug EventFn
	Info  EventFn
	Warn  EventFn
	Error EventFn
}

// Logger writes structured JSON logs and optionally fires event callbacks.
type Logger struct {
	discard   bool
	handler   slog.Handler
	traceIDFn TraceIDFn
}

// New constructs a Logger that writes JSON to w.
func New(w io.Writer, minLevel Level, serviceName string, traceIDFn TraceIDFn) *Logger {
	return build(w, minLevel, serviceName, traceIDFn, Events{})
}

// NewWithEvents constructs a Logger that writes JSON to w and fires event
// callbacks after each record — use these callbacks to forward logs to an
// external system such as Streamlogia.
func NewWithEvents(w io.Writer, minLevel Level, serviceName string, traceIDFn TraceIDFn, events Events) *Logger {
	return build(w, minLevel, serviceName, traceIDFn, events)
}

// NewWithHandler constructs a Logger backed by an arbitrary slog.Handler.
func NewWithHandler(h slog.Handler) *Logger {
	return &Logger{handler: h}
}

// NewStdLogger returns a standard-library Logger that wraps this Logger.
func NewStdLogger(l *Logger, level Level) *log.Logger {
	return slog.NewLogLogger(l.handler, slog.Level(level))
}

// Debug logs at DEBUG level.
func (l *Logger) Debug(ctx context.Context, msg string, args ...any) {
	l.write(ctx, LevelDebug, 3, msg, args...)
}

// Info logs at INFO level.
func (l *Logger) Info(ctx context.Context, msg string, args ...any) {
	l.write(ctx, LevelInfo, 3, msg, args...)
}

// Warn logs at WARN level.
func (l *Logger) Warn(ctx context.Context, msg string, args ...any) {
	l.write(ctx, LevelWarn, 3, msg, args...)
}

// Error logs at ERROR level.
func (l *Logger) Error(ctx context.Context, msg string, args ...any) {
	l.write(ctx, LevelError, 3, msg, args...)
}

func (l *Logger) write(ctx context.Context, level Level, caller int, msg string, args ...any) {
	if l.discard {
		return
	}

	slogLevel := slog.Level(level)
	if !l.handler.Enabled(ctx, slogLevel) {
		return
	}

	var pcs [1]uintptr
	runtime.Callers(caller, pcs[:])
	r := slog.NewRecord(time.Now(), slogLevel, msg, pcs[0])

	if l.traceIDFn != nil {
		args = append(args, "trace_id", l.traceIDFn(ctx))
	}
	r.Add(args...)

	l.handler.Handle(ctx, r) //nolint:errcheck
}

// build constructs the full handler chain: JSON → optional eventHandler → service attr.
func build(w io.Writer, minLevel Level, serviceName string, traceIDFn TraceIDFn, events Events) *Logger {
	replaceAttr := func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.SourceKey {
			if src, ok := a.Value.Any().(*slog.Source); ok {
				return slog.Attr{
					Key:   "file",
					Value: slog.StringValue(fmt.Sprintf("%s:%d", filepath.Base(src.File), src.Line)),
				}
			}
		}
		return a
	}

	h := slog.Handler(slog.NewJSONHandler(w, &slog.HandlerOptions{
		AddSource:   true,
		Level:       slog.Level(minLevel),
		ReplaceAttr: replaceAttr,
	}))

	if events.Debug != nil || events.Info != nil || events.Warn != nil || events.Error != nil {
		h = &eventHandler{inner: h, events: events}
	}

	h = h.WithAttrs([]slog.Attr{
		{Key: "service", Value: slog.StringValue(serviceName)},
	})

	return &Logger{
		discard:   w == io.Discard,
		handler:   h,
		traceIDFn: traceIDFn,
	}
}

// =============================================================================
// eventHandler — fires Events callbacks after delegating to the inner handler.

type eventHandler struct {
	inner  slog.Handler
	events Events
}

func (h *eventHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *eventHandler) Handle(ctx context.Context, r slog.Record) error {
	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}

	rec := toRecord(r)
	switch Level(r.Level) {
	case LevelDebug:
		if h.events.Debug != nil {
			h.events.Debug(ctx, rec)
		}
	case LevelInfo:
		if h.events.Info != nil {
			h.events.Info(ctx, rec)
		}
	case LevelWarn:
		if h.events.Warn != nil {
			h.events.Warn(ctx, rec)
		}
	case LevelError:
		if h.events.Error != nil {
			h.events.Error(ctx, rec)
		}
	}

	return nil
}

func (h *eventHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &eventHandler{inner: h.inner.WithAttrs(attrs), events: h.events}
}

func (h *eventHandler) WithGroup(name string) slog.Handler {
	return &eventHandler{inner: h.inner.WithGroup(name), events: h.events}
}

func toRecord(r slog.Record) Record {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	return Record{
		Time:       r.Time,
		Message:    r.Message,
		Level:      Level(r.Level),
		Attributes: attrs,
	}
}
