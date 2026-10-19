// Package server wires the API and the web UI into one HTTP handler that
// speaks Connect, gRPC and gRPC-Web on the same port.
package server

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"

	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/obs"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Request size limits. Models can be large; predictions are small. A
// server can take larger models (see api.Service.MaxModelBytes).
const (
	maxModelBytes   = 256 << 20
	maxPredictBytes = 16 << 20
)

// Option changes how Handler serves.
type Option func(*options)

type options struct {
	metricsToken string
}

// WithMetricsToken makes /metrics require "Authorization: Bearer <token>".
func WithMetricsToken(token string) Option {
	return func(o *options) { o.metricsToken = token }
}

// Handler returns the server's root handler.
func Handler(svc *api.Service, st *store.Store, ui fs.FS, log *slog.Logger, opts ...Option) http.Handler {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	mux := http.NewServeMux()
	metrics := obs.NewMetrics()

	// Every RPC is counted and timed, then goes through the auth
	// interceptor, which signs callers in from their cookie or API key and
	// checks the procedure's permission.
	guard := connect.WithInterceptors(metrics.Interceptor(), auth.Interceptor(st, svc.MustEnroll))
	modelBytes := max(svc.MaxModelBytes, maxModelBytes)
	mux.Handle(mimirv1connect.NewModelServiceHandler(svc, guard, connect.WithReadMaxBytes(modelBytes)))
	mux.Handle(mimirv1connect.NewPredictServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(mimirv1connect.NewAuthServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(mimirv1connect.NewUserServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(mimirv1connect.NewApiKeyServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(mimirv1connect.NewAuditServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(mimirv1connect.NewWebhookServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(mimirv1connect.NewSettingsServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(mimirv1connect.NewDatasetServiceHandler(svc, guard, connect.WithReadMaxBytes(maxModelBytes)))
	mux.Handle(mimirv1connect.NewTemplateServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes), connect.WithSendMaxBytes(maxModelBytes)))
	mux.Handle(mimirv1connect.NewTrainingServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	mux.Handle(mimirv1connect.NewTeamServiceHandler(svc, guard, connect.WithReadMaxBytes(maxPredictBytes)))
	// Workers authenticate inside Link with their token; the interceptor
	// guards the unary calls as usual.
	mux.Handle(mimirv1connect.NewWorkerServiceHandler(svc, guard, connect.WithReadMaxBytes(modelBytes), connect.WithSendMaxBytes(modelBytes)))
	mux.Handle("GET /api/backup", backupHandler(st, log))
	mux.Handle(grpchealth.NewHandler(newHealthChecker(st.Ping), connect.WithInterceptors(metrics.Interceptor())))
	mux.Handle("GET /healthz", liveHandler())
	mux.Handle("GET /readyz", readyHandler(st.Ping, log))
	mux.Handle("GET /metrics", metrics.Handler(o.metricsToken))
	mux.Handle("/", uiHandler(ui))

	uploads := newUploadGate(svc.MaxUploads, smallUpload)
	return obs.RequestIDs(metrics.HTTP(logRequests(harden(uploads.wrap(svc.WithConn(mux)), svc), log), route))
}

// route names a request's path for the HTTP metrics: one of a fixed set,
// so made-up URLs don't add series. RPCs are counted per procedure by the
// interceptor as well.
func route(r *http.Request) string {
	p := r.URL.Path
	switch {
	case p == "/healthz", p == "/readyz", p == "/metrics", p == "/api/backup":
		return p
	case strings.HasPrefix(p, "/mimir.v1."), strings.HasPrefix(p, "/grpc.health.v1."):
		return "rpc"
	}
	return "ui"
}

// harden refuses cross-site requests that carry a session cookie and adds
// the response headers that keep the UI from being framed or sniffed.
// Connect's JSON calls already need a CORS preflight that no other site
// passes; this is the second lock on the same door.
//
// HSTS goes out whenever the request came over HTTPS: TLS served here, or
// a trusted proxy's X-Forwarded-Proto.
func harden(next http.Handler, svc *api.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; worker-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'")
		if svc.Proxies.HTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if auth.SessionToken(r.Header) != "" && !auth.SameOrigin(r.Header, r.Host) {
			http.Error(w, "Cross-site requests can't use your sign-in.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// backupHandler sends an admin a pg_dump of the server's database, in
// pg_dump's custom format (restore it with pg_restore). It's a plain
// download rather than an RPC so the browser can save it. The dump goes to
// a temporary file first, so a pg_dump that fails half way is an error
// rather than a truncated download.
func backupHandler(st *store.Store, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := auth.Authenticate(r.Context(), st, r.Header, r.RemoteAddr)
		if err != nil || p == nil || p.Session == nil || p.Session.Pending || !p.Can(auth.SettingsManage) {
			http.Error(w, "Sign in as an admin to download a backup.", http.StatusForbidden)
			return
		}
		dir, err := os.MkdirTemp("", "mimir-backup-*")
		if err != nil {
			http.Error(w, "Couldn't make the backup.", http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "mimir.dump")
		if err := st.Backup(r.Context(), path); err != nil {
			if errors.Is(err, store.ErrBackupUnavailable) {
				http.Error(w, "This server can't make backups because pg_dump isn't installed where it runs. Back up the database with pg_dump --format=custom instead, or install the PostgreSQL client tools on the server.", http.StatusNotImplemented)
				return
			}
			log.ErrorContext(r.Context(), "backup", "error", err)
			http.Error(w, "Couldn't make the backup.", http.StatusInternalServerError)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "Couldn't make the backup.", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="mimir-`+time.Now().UTC().Format("2006-01-02-150405")+`.dump"`)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "mimir.dump", time.Now(), f)
	})
}

// Protocols enables HTTP/1.1 and HTTP/2, with TLS or without, so gRPC
// clients can connect either way. In production serve TLS (-tls-cert, or
// -tls-auto) or put a TLS-terminating proxy in front.
func Protocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetHTTP2(true)
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

// Flush passes through so gRPC and Connect streams, which connect-go
// checks for http.Flusher directly, can send as they go.
func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the rest.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests logs each request, with its request id from the context.
// Successful probes and scrapes are left out: they come every few seconds
// and say nothing.
func logRequests(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if quiet[r.URL.Path] && sw.status < 400 {
			return
		}
		log.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"proto", r.Proto,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
