package safe

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRun(t *testing.T) {
	before := Panics()
	if err := Run(quiet, "fine", func() {}); err != nil {
		t.Fatalf("Run of a function that returns = %v", err)
	}
	err := Run(quiet, "bad", func() { panic("boom") })
	if !errors.Is(err, ErrPanic) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Run of a panic = %v", err)
	}
	cause := errors.New("cause")
	if err := Run(quiet, "bad", func() { panic(cause) }); !errors.Is(err, cause) {
		t.Fatalf("a panic with an error should wrap it: %v", err)
	}
	if Panics()-before != 2 {
		t.Fatalf("counted %d panics, want 2", Panics()-before)
	}
}

// A handler that panics answers 500 and the server goes on answering.
func TestHTTP(t *testing.T) {
	h := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panic":
			panic("boom")
		case "/late":
			w.Write([]byte("partial"))
			panic("boom after writing")
		}
		w.Write([]byte("ok"))
	}), quiet)
	srv := httptest.NewServer(h)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/panic")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("panicking handler: status %d, want 500", res.StatusCode)
	}
	// Too late for a status: the connection is cut, and the client sees
	// a broken answer, not a whole one.
	if res, err := http.Get(srv.URL + "/late"); err == nil {
		_, err = io.ReadAll(res.Body)
		res.Body.Close()
		if err == nil {
			t.Fatal("a handler that panicked mid-answer should cut the connection")
		}
	}
	res, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "ok" {
		t.Fatalf("after the panics: %q", b)
	}
}

// A loop that panics starts again; one that returns is done.
func TestLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var runs atomic.Int32
	Loop(ctx, quiet, "flaky", func() {
		if runs.Add(1) < 3 {
			panic("not yet")
		}
	})
	if runs.Load() != 3 {
		t.Fatalf("ran %d times, want 3", runs.Load())
	}
}
