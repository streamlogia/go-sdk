package logingestor

import (
	"context"
	"fmt"
	"log/slog"
)

// SlogHandler adapts the Client to implement slog.Handler.
// This lets you use the standard library logger and have all output
// forwarded to the Log Ingestor service.
//
// Usage:
//
//	h := logingestor.NewSlogHandler(client)
//	logger := slog.New(h)
//
//	logger.Info("payment processed", "amount", 99.99, "currency", "USD")
type SlogHandler struct {
	client *Client
	attrs  []slog.Attr
	group  string
}

// NewSlogHandler wraps the client as a slog.Handler.
func NewSlogHandler(c *Client) *SlogHandler {
	return &SlogHandler{client: c}
}

// Enabled reports whether the handler handles records at the given level.
// All levels are enabled; filter at the call site or the server side.
func (h *SlogHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

// Handle converts an slog.Record into a log entry and sends it immediately.
func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	meta := make(map[string]any, r.NumAttrs()+len(h.attrs))

	for _, a := range h.attrs {
		meta[attrKey(h.group, a)] = attrVal(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		meta[attrKey(h.group, a)] = attrVal(a)
		return true
	})

	h.client.send(slogLevel(r.Level), r.Message, meta, nil)
	return nil
}

// WithAttrs returns a new handler that includes the given attributes on every entry.
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(combined, h.attrs)
	copy(combined[len(h.attrs):], attrs)
	return &SlogHandler{client: h.client, attrs: combined, group: h.group}
}

// WithGroup returns a new handler that namespaces attribute keys under the group.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	prefix := name
	if h.group != "" {
		prefix = h.group + "." + name
	}
	return &SlogHandler{client: h.client, attrs: h.attrs, group: prefix}
}

// MultiHandler fans out every log record to all provided handlers.
// Use this when you want logs going to both stdout and the ingestor.
//
//	logger := slog.New(logingestor.MultiHandler(
//	    slog.NewTextHandler(os.Stdout, nil),
//	    logingestor.NewSlogHandler(client),
//	))
type MultiHandler []slog.Handler

func (m MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(MultiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m MultiHandler) WithGroup(name string) slog.Handler {
	out := make(MultiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}

func slogLevel(l slog.Level) Level {
	switch {
	case l >= slog.LevelError:
		return LevelError
	case l >= slog.LevelWarn:
		return LevelWarn
	case l >= slog.LevelInfo:
		return LevelInfo
	default:
		return LevelDebug
	}
}

func attrKey(group string, a slog.Attr) string {
	if group != "" {
		return group + "." + a.Key
	}
	return a.Key
}

func attrVal(a slog.Attr) any {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		m := make(map[string]any)
		for _, sub := range v.Group() {
			m[sub.Key] = attrVal(sub)
		}
		return m
	default:
		return fmt.Sprintf("%v", v.Any())
	}
}
