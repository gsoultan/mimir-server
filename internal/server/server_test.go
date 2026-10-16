package server_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/server"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir/nn"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := newUnstarted(t)
	srv.Start()
	return srv
}

func newUnstarted(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>mimir</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	srv := httptest.NewUnstartedServer(server.Handler(api.New(st, log), ui, log))
	srv.Config.Protocols = server.Protocols()
	t.Cleanup(srv.Close)
	return srv
}

func weights(t *testing.T, name string) []byte {
	t.Helper()
	a, ok := arch.Lookup(name)
	if !ok {
		t.Fatalf("no architecture %q", name)
	}
	var buf bytes.Buffer
	if err := nn.Save(context.Background(), &buf, a.New()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeUnknown
}

func TestModelLifecycle(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	models := mimirv1connect.NewModelServiceClient(srv.Client(), srv.URL)
	predict := mimirv1connect.NewPredictServiceClient(srv.Client(), srv.URL)

	archs, err := models.ListArchitectures(ctx, connect.NewRequest(&mimirv1.ListArchitecturesRequest{}))
	if err != nil || len(archs.Msg.GetArchitectures()) == 0 {
		t.Fatalf("ListArchitectures = %v, %v", archs, err)
	}

	for _, name := range []string{"mnist-mlp", "mnist-cnn"} {
		t.Run(name, func(t *testing.T) {
			created, err := models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{
				Name:         "  Digits  ",
				Architecture: name,
				Weights:      weights(t, name),
			}))
			if err != nil {
				t.Fatal(err)
			}
			m := created.Msg.GetModel()
			if m.GetName() != "Digits" || m.GetStatus() != mimirv1.ModelStatus_MODEL_STATUS_READY {
				t.Fatalf("CreateModel = %v", m)
			}

			res, err := predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{
				ModelId: m.GetId(),
				Input:   &mimirv1.Tensor{Shape: []int64{1, 28, 28}, Values: make([]float32, 784)},
			}))
			if err != nil {
				t.Fatal(err)
			}
			var sum float64
			for _, p := range res.Msg.GetProbabilities().GetValues() {
				sum += float64(p)
			}
			if n := len(res.Msg.GetProbabilities().GetValues()); n != 10 || math.Abs(sum-1) > 1e-4 {
				t.Fatalf("output has %d values summing to %v", n, sum)
			}
			if g := res.Msg.GetGuesses(); len(g) != 3 || g[0].GetProbability() < g[1].GetProbability() {
				t.Fatalf("guesses = %v", g)
			}

			if _, err := models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: m.GetId()})); err != nil {
				t.Fatal(err)
			}
			_, err = predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{
				ModelId: m.GetId(),
				Input:   &mimirv1.Tensor{Values: make([]float32, 784)},
			}))
			if code(err) != connect.CodeNotFound {
				t.Fatalf("Predict after delete: %v", err)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	models := mimirv1connect.NewModelServiceClient(srv.Client(), srv.URL)
	predict := mimirv1connect.NewPredictServiceClient(srv.Client(), srv.URL)

	created, err := models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{
		Name: "Digits", Architecture: "mnist-mlp", Weights: weights(t, "mnist-mlp"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetModel().GetId()

	tests := []struct {
		name string
		call func() error
		want connect.Code
	}{
		{"wrong input size", func() error {
			_, err := predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: id, Input: &mimirv1.Tensor{Values: make([]float32, 5)}}))
			return err
		}, connect.CodeInvalidArgument},
		{"unknown model", func() error {
			_, err := predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: "0190a5c6-0000-7000-8000-000000000000"}))
			return err
		}, connect.CodeNotFound},
		{"malformed id", func() error {
			_, err := models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: "../etc/passwd"}))
			return err
		}, connect.CodeNotFound},
		{"weights for another type", func() error {
			_, err := models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "x", Architecture: "mnist-cnn", Weights: weights(t, "mnist-mlp")}))
			return err
		}, connect.CodeInvalidArgument},
		{"garbage weights", func() error {
			_, err := models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "x", Architecture: "mnist-mlp", Weights: []byte("nope")}))
			return err
		}, connect.CodeInvalidArgument},
		{"unknown architecture", func() error {
			_, err := models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "x", Architecture: "gpt", Weights: []byte("x")}))
			return err
		}, connect.CodeInvalidArgument},
		{"empty name", func() error {
			_, err := models.UpdateModel(ctx, connect.NewRequest(&mimirv1.UpdateModelRequest{Id: id, Name: "   "}))
			return err
		}, connect.CodeInvalidArgument},
		{"bad top_k", func() error {
			_, err := predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: id, TopK: -1, Input: &mimirv1.Tensor{Values: make([]float32, 784)}}))
			return err
		}, connect.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := code(tt.call()); got != tt.want {
				t.Fatalf("code = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGRPCAndHealth(t *testing.T) {
	srv := newServer(t)
	web := mimirv1connect.NewModelServiceClient(srv.Client(), srv.URL, connect.WithGRPCWeb())
	if _, err := web.ListModels(context.Background(), connect.NewRequest(&mimirv1.ListModelsRequest{})); err != nil {
		t.Fatalf("gRPC-Web: %v", err)
	}

	// Plain gRPC needs HTTP/2; the server accepts it without TLS.
	h2c := new(http.Protocols)
	h2c.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: h2c}}
	grpc := mimirv1connect.NewModelServiceClient(client, srv.URL, connect.WithGRPC())
	if _, err := grpc.ListArchitectures(context.Background(), connect.NewRequest(&mimirv1.ListArchitecturesRequest{})); err != nil {
		t.Fatalf("gRPC over h2c: %v", err)
	}
	res, err := http.Post(srv.URL+"/grpc.health.v1.Health/Check", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", res.StatusCode)
	}
}

func TestWebUI(t *testing.T) {
	srv := newServer(t)
	tests := []struct {
		path   string
		status int
		cache  string
	}{
		{"/", 200, "no-cache"},
		{"/models/123", 200, "no-cache"},
		{"/assets/app.js", 200, "public, max-age=31536000, immutable"},
		{"/assets/missing.js", 404, ""},
	}
	for _, tt := range tests {
		res, err := http.Get(srv.URL + tt.path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tt.status || (tt.cache != "" && res.Header.Get("Cache-Control") != tt.cache) {
			t.Errorf("GET %s = %d %q", tt.path, res.StatusCode, res.Header.Get("Cache-Control"))
		}
	}
}
