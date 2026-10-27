package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"

	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/memguard"
)

// readyTimeout bounds the database ping behind a readiness check, so a
// hung database fails the probe rather than hanging it.
const readyTimeout = 2 * time.Second

// quiet are the paths whose successful requests aren't logged.
var quiet = map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}

// liveHandler answers /healthz: the process is up and serving HTTP. It
// checks nothing else, so a database outage doesn't get the process
// restarted; /readyz is the one that reports that.
func liveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})
}

// readyHandler answers /readyz: 200 when the database answers a ping
// within readyTimeout, 503 otherwise. When memory is short it says so on
// a second line but stays ready: the console keeps answering from the
// models it has loaded, and a single console taken out of its load
// balancer would answer nothing.
func readyHandler(ping func(context.Context) error, memory *memguard.Guard, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := ping(ctx); err != nil {
			log.WarnContext(r.Context(), "readyz: database unreachable", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("database unreachable\n"))
			return
		}
		w.Write([]byte("ok\n"))
		if l := memory.Level(); l != memguard.OK {
			fmt.Fprintf(w, "memory: %s (%s of %s)\n", l, memguard.Format(memory.Used()), memguard.Format(memory.Limit()))
		}
	})
}

// healthChecker answers the gRPC health protocol the same way as /readyz:
// the server and each of its services are SERVING while the database
// answers, NOT_SERVING when it does not.
type healthChecker struct {
	ping     func(context.Context) error
	services map[string]bool
}

func newHealthChecker(ping func(context.Context) error) *healthChecker {
	c := &healthChecker{ping: ping, services: map[string]bool{"": true}}
	for _, name := range []string{
		mimirv1connect.ModelServiceName,
		mimirv1connect.PredictServiceName,
		mimirv1connect.AuthServiceName,
		mimirv1connect.UserServiceName,
		mimirv1connect.ApiKeyServiceName,
		mimirv1connect.AuditServiceName,
		mimirv1connect.WebhookServiceName,
		mimirv1connect.SettingsServiceName,
		mimirv1connect.DatasetServiceName,
		mimirv1connect.TemplateServiceName,
		mimirv1connect.TrainingServiceName,
		mimirv1connect.BenchmarkServiceName,
		mimirv1connect.WorkerServiceName,
		mimirv1connect.TeamServiceName,
	} {
		c.services[name] = true
	}
	return c
}

func (c *healthChecker) Check(ctx context.Context, req *grpchealth.CheckRequest) (*grpchealth.CheckResponse, error) {
	if !c.services[req.Service] {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown service"))
	}
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	if err := c.ping(ctx); err != nil {
		return &grpchealth.CheckResponse{Status: grpchealth.StatusNotServing}, nil
	}
	return &grpchealth.CheckResponse{Status: grpchealth.StatusServing}, nil
}
