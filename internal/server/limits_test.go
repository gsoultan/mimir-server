package server_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

func TestLimitsAndUsage(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	m := createModel(t, own, "Digits", "mnist-mlp")
	ask := func(c clients) error {
		_, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: make([]float32, 784)}}))
		return err
	}

	// No limits by default.
	for range 5 {
		if err := ask(own); err != nil {
			t.Fatal(err)
		}
	}
	// A wrong-size input doesn't count.
	own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: []float32{1}}}))

	// Per minute, per key: three quick ones, then wait.
	if _, err := own.settings.UpdateLimits(ctx, connect.NewRequest(&mimirv1.UpdateLimitsRequest{Limits: &mimirv1.Limits{PredictionsPerMinute: -1}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative limit: %v", err)
	}
	if _, err := own.settings.UpdateLimits(ctx, connect.NewRequest(&mimirv1.UpdateLimitsRequest{Limits: &mimirv1.Limits{PredictionsPerMinute: 3}})); err != nil {
		t.Fatal(err)
	}
	key, err := own.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{Name: "shop", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if err != nil {
		t.Fatal(err)
	}
	app := newClients(srv, connect.WithInterceptors(bearer(key.Msg.GetSecret())))
	for i := range 3 {
		if err := ask(app); err != nil {
			t.Fatalf("app call %d: %v", i, err)
		}
	}
	err = ask(app)
	var ce *connect.Error
	if code(err) != connect.CodeResourceExhausted || !errors.As(err, &ce) || ce.Meta().Get("Retry-After") == "" {
		t.Fatalf("over the minute: %v", err)
	}
	// The person on the web has their own allowance.
	if err := ask(own); err != nil {
		t.Fatalf("web after key ran out: %v", err)
	}

	// Per month, per person, counting their keys: 5 + 3 + 1 so far.
	if _, err := own.settings.UpdateLimits(ctx, connect.NewRequest(&mimirv1.UpdateLimitsRequest{Limits: &mimirv1.Limits{PredictionsPerMonth: 10}})); err != nil {
		t.Fatal(err)
	}
	if err := ask(own); err != nil { // the 10th
		t.Fatalf("10th: %v", err)
	}
	if err := ask(own); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("11th: %v", err)
	}
	if err := ask(app); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("key after the month ran out: %v", err)
	}

	u, err := own.settings.GetUsage(ctx, connect.NewRequest(&mimirv1.GetUsageRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if u.Msg.GetTotal() != 10 || u.Msg.GetLimit() != 10 || len(u.Msg.GetRows()) != 2 {
		t.Fatalf("usage %v", u.Msg)
	}
	byKey := map[string]int64{}
	for _, r := range u.Msg.GetRows() {
		byKey[r.GetKeyName()] = r.GetPredictions()
	}
	if byKey[""] != 7 || byKey["shop"] != 3 {
		t.Fatalf("usage rows %v", byKey)
	}
	if _, err := own.settings.GetUsage(ctx, connect.NewRequest(&mimirv1.GetUsageRequest{Month: "October"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad month: %v", err)
	}

	// Others see only their own usage; viewers can't change limits.
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "v@example.com", Name: "V", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}}))
	v := signIn(t, srv, "v@example.com")
	if err := ask(v); err != nil {
		t.Fatalf("someone else's month: %v", err)
	}
	vu, _ := v.settings.GetUsage(ctx, connect.NewRequest(&mimirv1.GetUsageRequest{}))
	if vu.Msg.GetTotal() != 1 || len(vu.Msg.GetRows()) != 1 {
		t.Fatalf("own usage %v", vu.Msg)
	}
	if _, err := v.settings.UpdateLimits(ctx, connect.NewRequest(&mimirv1.UpdateLimitsRequest{Limits: &mimirv1.Limits{}})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("editor UpdateLimits: %v", err)
	}

	// Training runs at once.
	if _, err := own.settings.UpdateLimits(ctx, connect.NewRequest(&mimirv1.UpdateLimitsRequest{Limits: &mimirv1.Limits{TrainingRunsPerPerson: 1}})); err != nil {
		t.Fatal(err)
	}
	ds, err := v.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(50))}))
	if err != nil {
		t.Fatal(err)
	}
	run := func() error {
		_, err := v.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "A", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId()}))
		return err
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if err := run(); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("second run at once: %v", err)
	}
	runner.Tick(ctx)
	if err := run(); err != nil {
		t.Fatalf("after the first finished: %v", err)
	}
}

// bearer sends an API key with every call.
func bearer(secret string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+secret)
			return next(ctx, req)
		}
	})
}
