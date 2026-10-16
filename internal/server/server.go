// Package server wires the API and the web UI into one HTTP handler that
// speaks Connect, gRPC and gRPC-Web on the same port.
package server

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"

	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/api"
)

// Request size limits. Models can be large; predictions are small.
const (
	maxModelBytes   = 256 << 20
	maxPredictBytes = 16 << 20
)

// Handler returns the server's root handler.
func Handler(svc *api.Service, ui fs.FS, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.Handle(mimirv1connect.NewModelServiceHandler(svc, connect.WithReadMaxBytes(maxModelBytes)))
	mux.Handle(mimirv1connect.NewPredictServiceHandler(svc, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(grpchealth.NewHandler(grpchealth.NewStaticChecker(
		mimirv1connect.ModelServiceName,
		mimirv1connect.PredictServiceName,
	)))
	mux.Handle("/", uiHandler(ui))

	return logRequests(mux, log)
}

// Protocols enables HTTP/1.1 and unencrypted HTTP/2, so gRPC clients can
// connect without TLS. Put a TLS-terminating proxy in front in production.
func Protocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}

// uiHandler serves the single-page app. Unknown paths fall back to
// index.html so client-side routes survive a reload.
func uiHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(ui, name); errors.Is(err, fs.ErrNotExist) {
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
		}
		if _, err := fs.Stat(ui, "index.html"); err != nil {
			http.Error(w, "The web UI isn't built. Run `make web`, or use the API directly.", http.StatusNotFound)
			return
		}
		switch {
		case strings.HasPrefix(name, "assets/"):
			// Vite fingerprints these, so they never change.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + name
		if name == "index.html" {
			r2.URL.Path = "/"
		}
		files.ServeHTTP(w, r2)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush for streaming.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func logRequests(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		log.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"proto", r.Proto,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
