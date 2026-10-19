package server_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"github.com/jackc/pgx/v5"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/obs"
	"github.com/gsoultan/mimir-server/internal/server"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/testdb"
)

func get(t *testing.T, u string) (int, string) {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// TestHealthAndReadiness checks /healthz stays up while /readyz and the
// gRPC health check follow the database, which is then dropped.
func TestHealthAndReadiness(t *testing.T) {
	srv := newServer(t)
	health := grpchealth.NewClient(srv.Client(), srv.URL)
	ctx := context.Background()

	if code, body := get(t, srv.URL+"/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Fatalf("/healthz = %d %q", code, body)
	}
	if code, _ := get(t, srv.URL+"/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz with the database up = %d", code)
	}
	for _, svc := range []string{"", mimirv1connect.ModelServiceName} {
		if res, err := health.Check(ctx, &grpchealth.CheckRequest{Service: svc}); err != nil || res.Status != grpchealth.StatusServing {
			t.Fatalf("health check %q = %+v, %v", svc, res, err)
		}
	}
	if _, err := health.Check(ctx, &grpchealth.CheckRequest{Service: "no.such.Service"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown service = %v, want not_found", err)
	}

	// Take the database away under the running server.
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, os.Getenv(testdb.Env))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{strings.TrimPrefix(u.Path, "/")}.Sanitize()+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}

	if code, body := get(t, srv.URL+"/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz with the database gone = %d %q, want 503", code, body)
	}
	if res, err := health.Check(ctx, &grpchealth.CheckRequest{}); err != nil || res.Status != grpchealth.StatusNotServing {
		t.Errorf("health check with the database gone = %+v, %v; want NOT_SERVING", res, err)
	}
	if code, _ := get(t, srv.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("/healthz with the database gone = %d, want 200", code)
	}
}

// syncBuffer is a log sink safe for the server's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return new(syncBuffer) }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRequestIDAndMetrics checks a caller's X-Request-Id comes back on the
// response and reaches the log, that one is made when none is sent, and
// that /metrics counts the call by procedure.
func TestRequestIDAndMetrics(t *testing.T) {
	st, err := store.Open(context.Background(), testdb.URL(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logs := newSyncBuffer()
	log := slog.New(obs.NewLogHandler(slog.NewJSONHandler(logs, nil)))
	srv := httptest.NewServer(server.Handler(api.New(st, log), st, fstest.MapFS{}, log, server.WithMetricsToken("scrape")))
	t.Cleanup(srv.Close)

	client := mimirv1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	req := connect.NewRequest(&mimirv1.GetSetupStateRequest{})
	req.Header().Set("X-Request-Id", "test-req-42")
	res, err := client.GetSetupState(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Header().Get("X-Request-Id"); got != "test-req-42" {
		t.Errorf("response X-Request-Id = %q, want the caller's", got)
	}
	if !strings.Contains(logs.String(), `"request_id":"test-req-42"`) {
		t.Errorf("log does not carry the request id:\n%s", logs.String())
	}

	res, err = client.GetSetupState(context.Background(), connect.NewRequest(&mimirv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if id := res.Header().Get("X-Request-Id"); len(id) != 36 || !strings.Contains(logs.String(), `"request_id":"`+id+`"`) {
		t.Errorf("made-up request id %q not returned or logged", id)
	}

	if code, _ := get(t, srv.URL+"/metrics"); code != http.StatusUnauthorized {
		t.Errorf("/metrics without the token = %d, want 401", code)
	}
	mreq, _ := http.NewRequest("GET", srv.URL+"/metrics", nil)
	mreq.Header.Set("Authorization", "Bearer scrape")
	mres, err := http.DefaultClient.Do(mreq)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(mres.Body)
	mres.Body.Close()
	body := string(b)
	for _, want := range []string{
		`mimir_rpc_requests_total{code="ok",procedure="/mimir.v1.AuthService/GetSetupState"} 2`,
		`mimir_http_requests_total{code="200",method="POST",route="rpc"} 2`,
		`mimir_rpc_duration_seconds_count{procedure="/mimir.v1.AuthService/GetSetupState"} 2`,
		`mimir_http_request_duration_seconds_bucket{method="POST",route="rpc",le="+Inf"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
}
