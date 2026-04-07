package logingestor

import (
	"fmt"
	"net/http"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	status int
	size   int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.size += n
	return n, err
}

// HTTPMiddleware returns an http.Handler middleware that records one log entry
// per request. The entry captures method, path, status code, duration (ms),
// and optionally a request-id header.
//
// Usage with net/http:
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/", handler)
//	http.ListenAndServe(":8080", client.HTTPMiddleware(mux))
//
// Usage with chi:
//
//	r := chi.NewRouter()
//	r.Use(client.HTTPMiddleware)
func (c *Client) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		meta := map[string]any{
			"method":     r.Method,
			"path":       r.URL.Path,
			"status":     rw.status,
			"durationMs": duration.Milliseconds(),
			"remoteAddr": r.RemoteAddr,
			"userAgent":  r.UserAgent(),
		}
		if rid := r.Header.Get("X-Request-Id"); rid != "" {
			meta["requestId"] = rid
		}

		level := levelForStatus(rw.status)
		msg := fmt.Sprintf("%s %s %d (%dms)", r.Method, r.URL.Path, rw.status, duration.Milliseconds())
		c.enqueue(r.Context(), level, msg, meta, nil)
	})
}

func levelForStatus(status int) Level {
	switch {
	case status >= 500:
		return LevelError
	case status >= 400:
		return LevelWarn
	default:
		return LevelInfo
	}
}
