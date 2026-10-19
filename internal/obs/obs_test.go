package obs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRequestIDs(t *testing.T) {
	var seen string
	h := RequestIDs(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
	}))

	tests := []struct {
		name, sent string
		keep       bool
	}{
		{"kept", "abc-123_DEF.4:5", true},
		{"uuid", "0b8c1f1e-5f5a-4bd4-9d0f-1c2a3b4c5d6e", true},
		{"none", "", false},
		{"spaces", "a b", false},
		{"newline", "a\nlevel=error", false},
		{"too long", strings.Repeat("a", 129), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.sent != "" {
				req.Header.Set(RequestIDHeader, tt.sent)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get(RequestIDHeader)
			if got == "" || got != seen {
				t.Fatalf("response id %q, context id %q", got, seen)
			}
			if tt.keep != (got == tt.sent) {
				t.Errorf("sent %q, got %q, want kept=%v", tt.sent, got, tt.keep)
			}
			if !validRequestID(got) {
				t.Errorf("generated id %q is not valid", got)
			}
		})
	}
}

func TestLogHandlerAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil))).With("component", "test")
	log.InfoContext(WithRequestID(context.Background(), "req-1"), "hello")
	log.InfoContext(context.Background(), "bare")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "request_id=req-1") || !strings.Contains(lines[0], "component=test") {
		t.Errorf("first line = %q", lines[0])
	}
	if strings.Contains(lines[1], "request_id") {
		t.Errorf("line without a request carries an id: %q", lines[1])
	}
}

func TestHTTPMetrics(t *testing.T) {
	m := NewMetrics()
	h := m.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			http.Error(w, "no", http.StatusInternalServerError)
		}
	}), func(r *http.Request) string { return r.URL.Path })
	for _, p := range []string{"/ok", "/ok", "/boom"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("BREW", "/ok", nil))
	if n := testutil.ToFloat64(m.httpReqs.WithLabelValues("GET", "/ok", "200")); n != 2 {
		t.Errorf("GET /ok 200 = %v, want 2", n)
	}
	if n := testutil.ToFloat64(m.httpErrs.WithLabelValues("GET", "/boom")); n != 1 {
		t.Errorf("errors on /boom = %v, want 1", n)
	}
	if n := testutil.ToFloat64(m.httpReqs.WithLabelValues("other", "/ok", "200")); n != 1 {
		t.Errorf("unknown method counted as %v under other, want 1", n)
	}
	if n := testutil.CollectAndCount(m.httpDur); n != 3 {
		t.Errorf("duration series = %d, want 3", n)
	}
}

type fakeSpecRequest struct {
	connect.AnyRequest
	spec connect.Spec
}

func (r fakeSpecRequest) Spec() connect.Spec { return r.spec }

func TestRPCMetrics(t *testing.T) {
	m := NewMetrics()
	wrap := m.Interceptor().WrapUnary
	ok := wrap(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) { return nil, nil })
	bad := wrap(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("gone"))
	})
	req := fakeSpecRequest{spec: connect.Spec{Procedure: "/mimir.v1.X/Y"}}
	ok(context.Background(), req)
	bad(context.Background(), req)
	if n := testutil.ToFloat64(m.rpcReqs.WithLabelValues("/mimir.v1.X/Y", "ok")); n != 1 {
		t.Errorf("ok calls = %v", n)
	}
	if n := testutil.ToFloat64(m.rpcErrs.WithLabelValues("/mimir.v1.X/Y", "not_found")); n != 1 {
		t.Errorf("not_found errors = %v", n)
	}
}

func TestMetricsToken(t *testing.T) {
	h := NewMetrics().Handler("s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Errorf("with token: %d", rec.Code)
	}
}
