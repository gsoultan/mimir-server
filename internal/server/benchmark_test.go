package server_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// waitScore waits for a model's score on a benchmark to finish, since
// scoring runs in the background.
func waitScore(t *testing.T, c clients, benchID, modelID string) *mimirv1.BenchmarkScore {
	t.Helper()
	ctx := context.Background()
	for range 300 {
		got, err := c.bench.GetBenchmark(ctx, connect.NewRequest(&mimirv1.GetBenchmarkRequest{Id: benchID}))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range got.Msg.GetScores() {
			if s.GetModelId() != modelID {
				continue
			}
			switch s.GetStatus() {
			case mimirv1.JobStatus_JOB_STATUS_SUCCEEDED, mimirv1.JobStatus_JOB_STATUS_FAILED:
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the score on %s never finished", benchID)
	return nil
}

func TestBenchmarkLeaderboard(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(300))}))
	if err != nil {
		t.Fatal(err)
	}
	dsID := ds.Msg.GetDataset().GetId()

	// A benchmark freezes a fifth of the rows and says what it scores on.
	b, err := own.bench.CreateBenchmark(ctx, connect.NewRequest(&mimirv1.CreateBenchmarkRequest{Name: "Fruit test", DatasetId: dsID}))
	if err != nil {
		t.Fatal(err)
	}
	bm := b.Msg.GetBenchmark()
	if bm.GetRows() != 60 || bm.GetKind() != "class" || bm.GetTarget() != "kind" || bm.GetMetric() != "right answers" || bm.GetLowerIsBetter() {
		t.Fatalf("benchmark %v", bm)
	}

	// Training on the dataset now leaves the test rows out, so the run
	// learns from the rest.
	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit", Architecture: "table-mlp", DatasetId: dsID, Settings: &mimirv1.TrainingSettings{Epochs: 6, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	job, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if err != nil || job.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		t.Fatalf("job %v, %v", job, err)
	}
	if got := job.Msg.GetJob().GetReport().GetExamples(); got != 48 {
		t.Fatalf("scored on %d examples, want 48 of the 240 left", got)
	}
	modelID := job.Msg.GetJob().GetModelId()

	// The run's model is scored on the benchmark on its own, and leads it.
	score := waitScore(t, own, bm.GetId(), modelID)
	if score.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || score.GetRank() != 1 || score.GetStale() {
		t.Fatalf("score %v", score)
	}
	if r := score.GetReport(); r.GetExamples() != 60 || r.GetAccuracy() < 0.9 || len(r.GetClasses()) != 2 {
		t.Fatalf("report %v", score.GetReport())
	}
	if score.GetHeadline() != score.GetReport().GetAccuracy() {
		t.Fatalf("headline %v, report %v", score.GetHeadline(), score.GetReport())
	}
	got, err := own.bench.GetBenchmark(ctx, connect.NewRequest(&mimirv1.GetBenchmarkRequest{Id: bm.GetId()}))
	if err != nil || got.Msg.GetBenchmark().GetScored() != 1 || got.Msg.GetBenchmark().GetLeaderName() != "Fruit" {
		t.Fatalf("leaderboard %v, %v", got, err)
	}
	if len(got.Msg.GetCandidates()) != 0 {
		t.Fatalf("candidates %v", got.Msg.GetCandidates())
	}

	// Scoring the same model again replaces its one row.
	if _, err := own.bench.ScoreModels(ctx, connect.NewRequest(&mimirv1.ScoreModelsRequest{BenchmarkId: bm.GetId(), ModelIds: []string{modelID}})); err != nil {
		t.Fatal(err)
	}
	waitScore(t, own, bm.GetId(), modelID)
	got, err = own.bench.GetBenchmark(ctx, connect.NewRequest(&mimirv1.GetBenchmarkRequest{Id: bm.GetId()}))
	if err != nil || len(got.Msg.GetScores()) != 1 {
		t.Fatalf("scores %v, %v", got, err)
	}

	// A model that answers the wrong kind of question is refused.
	var nums strings.Builder
	nums.WriteString("x,y\n")
	for i := range 200 {
		fmt.Fprintf(&nums, "%d,%d\n", i%50, 2*(i%50)+10)
	}
	nds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Line", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(nums.String())}))
	if err != nil {
		t.Fatal(err)
	}
	nj, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Line", Architecture: "number-mlp", DatasetId: nds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	njob, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: nj.Msg.GetJob().GetId()}))
	if _, err := own.bench.ScoreModels(ctx, connect.NewRequest(&mimirv1.ScoreModelsRequest{BenchmarkId: bm.GetId(), ModelIds: []string{njob.Msg.GetJob().GetModelId()}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("number model on a label test: %v", err)
	}

	// A number dataset's benchmark scores on the average miss, lower
	// being better.
	nb, err := own.bench.CreateBenchmark(ctx, connect.NewRequest(&mimirv1.CreateBenchmarkRequest{Name: "Line test", DatasetId: nds.Msg.GetDataset().GetId(), Share: 0.1}))
	if err != nil {
		t.Fatal(err)
	}
	if nb.Msg.GetBenchmark().GetKind() != "number" || !nb.Msg.GetBenchmark().GetLowerIsBetter() || nb.Msg.GetBenchmark().GetRows() != 20 {
		t.Fatalf("number benchmark %v", nb.Msg.GetBenchmark())
	}
	nmodel := njob.Msg.GetJob().GetModelId()
	if _, err := own.bench.ScoreModels(ctx, connect.NewRequest(&mimirv1.ScoreModelsRequest{BenchmarkId: nb.Msg.GetBenchmark().GetId(), ModelIds: []string{nmodel}})); err != nil {
		t.Fatal(err)
	}
	ns := waitScore(t, own, nb.Msg.GetBenchmark().GetId(), nmodel)
	if ns.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || ns.GetReport().GetKind() != "number" || ns.GetHeadline() != ns.GetReport().GetMae() {
		t.Fatalf("number score %v", ns)
	}

	// Listing filters by dataset; deleting takes the scores with it.
	ls, err := own.bench.ListBenchmarks(ctx, connect.NewRequest(&mimirv1.ListBenchmarksRequest{DatasetId: dsID}))
	if err != nil || len(ls.Msg.GetBenchmarks()) != 1 || ls.Msg.GetBenchmarks()[0].GetLeaderName() != "Fruit" {
		t.Fatalf("list %v, %v", ls, err)
	}
	if _, err := own.bench.DeleteBenchmark(ctx, connect.NewRequest(&mimirv1.DeleteBenchmarkRequest{Id: bm.GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := own.bench.GetBenchmark(ctx, connect.NewRequest(&mimirv1.GetBenchmarkRequest{Id: bm.GetId()})); code(err) != connect.CodeNotFound {
		t.Fatalf("after delete: %v", err)
	}

	// Another team's benchmark reads as missing, and a viewer can't look.
	lab, err := own.teams.CreateTeam(ctx, connect.NewRequest(&mimirv1.CreateTeamRequest{Name: "Lab"}))
	if err != nil {
		t.Fatal(err)
	}
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "a@lab.test", Name: "Lab admin", Password: password, TeamId: lab.Msg.GetTeam().GetId(), Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_ADMIN}}))
	if _, err := signIn(t, srv, "a@lab.test").bench.GetBenchmark(ctx, connect.NewRequest(&mimirv1.GetBenchmarkRequest{Id: nb.Msg.GetBenchmark().GetId()})); code(err) != connect.CodeNotFound {
		t.Fatalf("other team: %v", err)
	}
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "v@example.com", Name: "V", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if _, err := signIn(t, srv, "v@example.com").bench.ListBenchmarks(ctx, connect.NewRequest(&mimirv1.ListBenchmarksRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer: %v", err)
	}
}

func TestBenchmarkFromUploadedRows(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	dsID := ds.Msg.GetDataset().GetId()

	// Rows of its own: the dataset keeps every row, and the test is
	// scored on what was sent.
	rows := "width,height,kind\n1.0,1.0,apple\n-1.0,-1.0,pear\n0.9,1.1,apple\n"
	b, err := own.bench.CreateBenchmark(ctx, connect.NewRequest(&mimirv1.CreateBenchmarkRequest{Name: "Mine", DatasetId: dsID, Csv: []byte(rows)}))
	if err != nil {
		t.Fatal(err)
	}
	if b.Msg.GetBenchmark().GetRows() != 3 {
		t.Fatalf("rows %v", b.Msg.GetBenchmark())
	}

	// Rows missing a column the dataset has are refused.
	if _, err := own.bench.CreateBenchmark(ctx, connect.NewRequest(&mimirv1.CreateBenchmarkRequest{Name: "Short", DatasetId: dsID, Csv: []byte("width,kind\n1.0,apple\n")})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing column: %v", err)
	}

	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit", Architecture: "table-mlp", DatasetId: dsID, Settings: &mimirv1.TrainingSettings{Epochs: 6, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	job, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if got := job.Msg.GetJob().GetReport().GetExamples(); got != 40 {
		t.Fatalf("scored on %d examples, want 40 of the 200 kept", got)
	}
	s := waitScore(t, own, b.Msg.GetBenchmark().GetId(), job.Msg.GetJob().GetModelId())
	if s.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || s.GetReport().GetExamples() != 3 {
		t.Fatalf("score %v", s)
	}
}
