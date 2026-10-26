package server_test

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

func TestCompareJobs(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	teach := func(name string, s *mimirv1.TrainingSettings) string {
		t.Helper()
		j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: name, Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: s}))
		if err != nil {
			t.Fatal(err)
		}
		return j.Msg.GetJob().GetId()
	}
	slow := teach("Slow", &mimirv1.TrainingSettings{Epochs: 1, BatchSize: 32, LearningRate: 0.0001})
	fast := teach("Fast", &mimirv1.TrainingSettings{Epochs: 6, BatchSize: 32, LearningRate: 0.01})
	runner.Tick(ctx)
	runner.Tick(ctx)

	// Runs come back in the order asked, with every pass, what sets them
	// apart and the best of them. Repeats are dropped.
	res, err := own.training.CompareJobs(ctx, connect.NewRequest(&mimirv1.CompareJobsRequest{Ids: []string{fast, slow, fast}}))
	if err != nil {
		t.Fatal(err)
	}
	js := res.Msg.GetJobs()
	if len(js) != 2 || js[0].GetId() != fast || js[1].GetId() != slow || len(js[0].GetHistory()) != 6 || len(js[1].GetHistory()) != 1 || js[0].GetDatasetName() != "Fruit" {
		t.Fatalf("jobs %v", js)
	}
	if !slices.Equal(res.Msg.GetDiffers(), []string{"epochs", "learning_rate"}) {
		t.Fatalf("differs %v", res.Msg.GetDiffers())
	}
	want := fast
	if js[1].GetAccuracy() > js[0].GetAccuracy() {
		want = slow
	}
	if res.Msg.GetBestId() != want {
		t.Fatalf("best %q, want %q", res.Msg.GetBestId(), want)
	}

	// One run, too many, or one that isn't there is refused.
	if _, err := own.training.CompareJobs(ctx, connect.NewRequest(&mimirv1.CompareJobsRequest{Ids: []string{fast, fast}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("one run: %v", err)
	}
	if _, err := own.training.CompareJobs(ctx, connect.NewRequest(&mimirv1.CompareJobsRequest{Ids: []string{"a", "b", "c", "d", "e", "f", "g"}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("seven runs: %v", err)
	}
	if _, err := own.training.CompareJobs(ctx, connect.NewRequest(&mimirv1.CompareJobsRequest{Ids: []string{fast, "nope"}})); code(err) != connect.CodeNotFound {
		t.Fatalf("missing run: %v", err)
	}

	// Another team's runs are not found, and viewers can't look.
	lab, err := own.teams.CreateTeam(ctx, connect.NewRequest(&mimirv1.CreateTeamRequest{Name: "Lab"}))
	if err != nil {
		t.Fatal(err)
	}
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "a@lab.test", Name: "Lab admin", Password: password, TeamId: lab.Msg.GetTeam().GetId(), Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_ADMIN}}))
	if _, err := signIn(t, srv, "a@lab.test").training.CompareJobs(ctx, connect.NewRequest(&mimirv1.CompareJobsRequest{Ids: []string{fast, slow}})); code(err) != connect.CodeNotFound {
		t.Fatalf("other team: %v", err)
	}
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "v@example.com", Name: "V", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if _, err := signIn(t, srv, "v@example.com").training.CompareJobs(ctx, connect.NewRequest(&mimirv1.CompareJobsRequest{Ids: []string{fast, slow}})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer: %v", err)
	}
}
