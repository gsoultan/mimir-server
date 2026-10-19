// Package obs carries what operators need to see a request through the
// server: a request id on every response and log line, and RED metrics
// (rate, errors, duration) in the Prometheus format.
package obs

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// RequestIDHeader is the header a request id arrives and leaves in.
const RequestIDHeader = "X-Request-Id"

// maxRequestID bounds an id a caller sends; a longer one is replaced.
const maxRequestID = 128

type requestIDKey struct{}

// WithRequestID returns ctx carrying id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request id ctx carries, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// validRequestID accepts the ids proxies and clients commonly send (UUIDs,
// hex, base64) and nothing that could break a log line or a header.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestID {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':', c == '+', c == '/', c == '=':
		default:
			return false
		}
	}
	return true
}

// RequestIDs gives every request an id: the caller's X-Request-Id when it
// is a sensible one, otherwise a new UUID. The id goes back in the
// response's X-Request-Id and rides the request's context, where
// LogHandler adds it to every log line written with that context.
func RequestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewString()
			r.Header.Set(RequestIDHeader, id)
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

// LogHandler adds a request_id attribute to each record logged with a
// context that carries one (InfoContext, ErrorContext and so on).
type LogHandler struct {
	slog.Handler
}

// NewLogHandler wraps h.
func NewLogHandler(h slog.Handler) *LogHandler { return &LogHandler{Handler: h} }

// Handle implements slog.Handler.
func (h *LogHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r = r.Clone()
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{Handler: h.Handler.WithGroup(name)}
}
