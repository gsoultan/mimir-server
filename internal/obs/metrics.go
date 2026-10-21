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

	// Predictions the server runs itself, by engine: "interpreter" or
	// "mimir".
	predictions *prometheus.CounterVec   // questions answered, by engine and result
	batches     *prometheus.HistogramVec // questions per batch, by engine
	queueWait   *prometheus.HistogramVec // time a question waited for its batch, by engine
	batchDur    *prometheus.HistogramVec // time to run a batch, by engine
	loads       *prometheus.CounterVec   // models loaded, by kind and result
	loadDur     *prometheus.HistogramVec // time to load a model, by kind
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
	m.predictions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mimir_predictions_total",
		Help: "Questions the server's models answered, by engine (interpreter or mimir) and result (ok or error).",
	}, []string{"engine", "result"})
	m.batches = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mimir_prediction_batch_size",
		Help:    "Questions run together in one batch, by engine.",
		Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128},
	}, []string{"engine"})
	m.queueWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mimir_prediction_queue_seconds",
		Help:    "Time a question waited for a batch to start, by engine.",
		Buckets: []float64{.00005, .0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, 1},
	}, []string{"engine"})
	m.batchDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mimir_prediction_batch_seconds",
		Help:    "Time to run one batch of questions, by engine.",
		Buckets: []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"engine"})
	m.loads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mimir_model_loads_total",
		Help: "Models loaded into memory, by kind (built, file or language) and result (ok or error).",
	}, []string{"kind", "result"})
	m.loadDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mimir_model_load_seconds",
		Help:    "Time to load a model into memory, by kind.",
		Buckets: prometheus.DefBuckets,
	}, []string{"kind"})
	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpReqs, m.httpErrs, m.httpDur, m.rpcReqs, m.rpcErrs, m.rpcDur,
		m.predictions, m.batches, m.queueWait, m.batchDur, m.loads, m.loadDur,
	)
	return m
}

// ObserveBatch counts a batch of size questions that engine ran in took,
// each having waited for the matching duration of waited. It makes
// Metrics a runtime.Observer.
func (m *Metrics) ObserveBatch(engine string, size int, waited []time.Duration, took time.Duration, failed bool) {
	m.predictions.WithLabelValues(engine, result(failed)).Add(float64(size))
	m.batches.WithLabelValues(engine).Observe(float64(size))
	m.batchDur.WithLabelValues(engine).Observe(took.Seconds())
	q := m.queueWait.WithLabelValues(engine)
	for _, w := range waited {
		q.Observe(w.Seconds())
	}
}

// ObserveLoad counts a model of the given kind loaded in took.
func (m *Metrics) ObserveLoad(kind string, took time.Duration, failed bool) {
	m.loads.WithLabelValues(kind, result(failed)).Inc()
	m.loadDur.WithLabelValues(kind).Observe(took.Seconds())
}

func result(failed bool) string {
	if failed {
		return "error"
	}
	return "ok"
}

// WatchModelCache reports the models the server holds loaded, as usage
// returns them when Prometheus scrapes.
func (m *Metrics) WatchModelCache(usage func() (bytes int64, models int)) {
	m.reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "mimir_model_cache_bytes",
			Help: "Bytes of models held loaded, counted by the size of their files.",
		}, func() float64 { b, _ := usage(); return float64(b) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "mimir_models_loaded",
			Help: "Models held loaded.",
		}, func() float64 { _, n := usage(); return float64(n) }),
	)
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
