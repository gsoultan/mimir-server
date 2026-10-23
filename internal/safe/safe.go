// Package safe keeps a panic in one request, job or task from taking the
// whole process down: it becomes an error for that one piece of work, is
// logged with its stack, and counted.
package safe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
)

// ErrPanic is what a recovered panic becomes.
var ErrPanic = errors.New("internal error")

var panics atomic.Int64

// Panics returns how many panics were recovered since the process
// started, for metrics.
func Panics() int64 { return panics.Load() }

// Recover, deferred, turns a panic in the function deferring it into an
// error wrapping ErrPanic in *err, logging it with its stack as what
// failed. It must be deferred directly:
//
//	defer safe.Recover(log, "training job", &err)
func Recover(log *slog.Logger, what string, err *error) {
	p := recover()
	if p == nil {
		return
	}
	e := caught(log, context.Background(), what, p)
	if err != nil {
		*err = e
	}
}

// Run calls f and returns the panic it raised, if any, as an error
// wrapping ErrPanic.
func Run(log *slog.Logger, what string, f func()) (err error) {
	defer Recover(log, what, &err)
	f()
	return nil
}

// caught logs and counts a recovered panic p and returns it as an error.
func caught(log *slog.Logger, ctx context.Context, what string, p any) error {
	panics.Add(1)
	if log == nil {
		log = slog.Default()
	}
	log.ErrorContext(ctx, "panic recovered; the process carries on", "in", what, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
	if e, ok := p.(error); ok {
		return fmt.Errorf("%w in %s: %w", ErrPanic, what, e)
	}
	return fmt.Errorf("%w in %s: %v", ErrPanic, what, p)
}

// HTTP answers a request whose handler panicked with a 500, in Connect's
// error shape where the client speaks it, and keeps serving. A handler
// that already started its answer has its connection cut instead, as
// net/http does. http.ErrAbortHandler passes through: it is how a handler
// asks for exactly that.
func HTTP(next http.Handler, log *slog.Logger) http.Handler {
	errs := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &started{ResponseWriter: w}
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if p == http.ErrAbortHandler {
				panic(p)
			}
			caught(log, r.Context(), r.Method+" "+r.URL.Path, p)
			if sw.wrote {
				panic(http.ErrAbortHandler) // too late for a status; cut it
			}
			err := connect.NewError(connect.CodeInternal, errors.New("Something went wrong on the server. It's been logged; try again."))
			if errs.IsSupported(r) {
				errs.Write(w, r, err)
				return
			}
			http.Error(w, err.Message(), http.StatusInternalServerError)
		}()
		next.ServeHTTP(sw, r)
	})
}

// started notes whether the answer has begun.
type started struct {
	http.ResponseWriter
	wrote bool
}

func (s *started) WriteHeader(code int) {
	if code >= 200 {
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *started) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *started) Flush() {
	s.wrote = true
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *started) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Loop runs f until ctx ends, starting it again a second later when it
// panics, so a background loop such as a queue's never stops for good.
// f should return only when ctx ends.
func Loop(ctx context.Context, log *slog.Logger, what string, f func()) {
	for ctx.Err() == nil {
		if Run(log, what, f) == nil {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}
