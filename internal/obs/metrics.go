package obs

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the server's RED metrics in a registry of its own, so two
// servers in one process (as in tests) don't collide.
//
// Every HTTP request is counted by method, route and status. Routes are a
// small fixed set the caller maps paths to, so a client cannot grow the
// series by making up URLs. Connect, gRPC and gRPC-Web calls are counted
// again per procedure, with the RPC's own status code: a gRPC error is an
// HTTP 200.
type Metrics struct {
	reg      *prometheus.Registry
	httpReqs *prometheus.CounterVec
	httpErrs *prometheus.CounterVec
	httpDur  *prometheus.HistogramVec
	rpcReqs  *prometheus.CounterVec
	rpcErrs  *prometheus.CounterVec
	rpcDur   *prometheus.HistogramVec
}

// NewMetrics registers the server's metrics, and the Go runtime's and the
// process's, in a new registry.
func NewMetrics() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		httpReqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mimir_http_requests_total",
			Help: "HTTP requests served, by method, route and status code.",
		}, []string{"method", "route", "code"}),
		httpErrs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mimir_http_errors_total",
			Help: "HTTP requests answered with a 5xx status, by method and route.",
		}, []string{"method", "route"}),
		httpDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mimir_http_request_duration_seconds",
			Help:    "Time to serve an HTTP request, by method and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		rpcReqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mimir_rpc_requests_total",
			Help: "Connect, gRPC and gRPC-Web calls handled, by procedure and status code (ok or the Connect code).",
		}, []string{"procedure", "code"}),
		rpcErrs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mimir_rpc_errors_total",
			Help: "Calls that ended in an error, by procedure and Connect code.",
		}, []string{"procedure", "code"}),
		rpcDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mimir_rpc_duration_seconds",
			Help:    "Time to handle a call, by procedure; a stream counts until it ends.",
			Buckets: prometheus.DefBuckets,
		}, []string{"procedure"}),
	}
	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpReqs, m.httpErrs, m.httpDur, m.rpcReqs, m.rpcErrs, m.rpcDur,
	)
	return m
}

// Registry is where the metrics live, for tests and extra collectors.
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// Handler serves the metrics in the Prometheus text format. With a token,
// a scrape must send it as "Authorization: Bearer <token>".
func (m *Metrics) Handler(token string) http.Handler {
	h := promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
	if token == "" {
		return h
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// HTTP counts and times every request; route names the path's route.
func (m *Metrics) HTTP(next http.Handler, route func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			method, rt := methodLabel(r.Method), route(r)
			m.httpReqs.WithLabelValues(method, rt, strconv.Itoa(status)).Inc()
			if status >= 500 {
				m.httpErrs.WithLabelValues(method, rt).Inc()
			}
			m.httpDur.WithLabelValues(method, rt).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

// methodLabel keeps the method label to the standard methods.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return m
	}
	return "other"
}

// Interceptor counts and times Connect calls. Put it first, so calls the
// auth interceptor turns away are counted too. Only procedures a handler
// is registered for reach it, so the procedure label is bounded.
func (m *Metrics) Interceptor() connect.Interceptor { return rpcMetrics{m} }

type rpcMetrics struct{ m *Metrics }

func (i rpcMetrics) observe(procedure string, start time.Time, err error) {
	code := "ok"
	if err != nil {
		code = connect.CodeOf(err).String()
		if errors.Is(err, context.Canceled) {
			code = connect.CodeCanceled.String()
		}
		i.m.rpcErrs.WithLabelValues(procedure, code).Inc()
	}
	i.m.rpcReqs.WithLabelValues(procedure, code).Inc()
	i.m.rpcDur.WithLabelValues(procedure).Observe(time.Since(start).Seconds())
}

func (i rpcMetrics) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		start := time.Now()
		res, err := next(ctx, req)
		i.observe(req.Spec().Procedure, start, err)
		return res, err
	}
}

func (i rpcMetrics) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i rpcMetrics) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		i.observe(conn.Spec().Procedure, start, err)
		return err
	}
}

// statusWriter records the status code a handler sends.
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

// Flush passes through so streams can send as they go; connect-go checks
// for http.Flusher directly.
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
