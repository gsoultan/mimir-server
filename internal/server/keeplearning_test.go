package server_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// fruitDB makes a SQLite database outside the server's data folder with n
// rows of two blobs, like blobs() but in a table.
func fruitDB(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shop.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE fruit (width REAL, height REAL, kind TEXT)"); err != nil {
		t.Fatal(err)
	}
	addFruit(t, path, n)
	return path
}

func addFruit(t *testing.T, path string, n int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, line := range strings.Split(strings.TrimSpace(blobs(n)), "\n")[1:] {
		c := strings.Split(line, ",")
		if _, err := db.Exec("INSERT INTO fruit VALUES (?, ?, ?)", c[0], c[1], c[2]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTeachFromQueryAndKeepLearning(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	path := fruitDB(t, 200)
	conn := &mimirv1.Connection{Driver: "sqlite", Dsn: path}
	query := "SELECT width, height, kind FROM fruit"

	// Try the query first.
	tq, err := own.datasets.TestQuery(ctx, connect.NewRequest(&mimirv1.TestQueryRequest{Connection: conn, Query: query}))
	if err != nil || strings.Join(tq.Msg.GetColumns(), ",") != "width,height,kind" || tq.Msg.GetRows() != 200 || len(tq.Msg.GetSample()) != 5 {
		t.Fatalf("TestQuery = %v, %v", tq, err)
	}
	if _, err := own.datasets.TestQuery(ctx, connect.NewRequest(&mimirv1.TestQueryRequest{Connection: conn, Query: "SELECT * FROM nothing"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad query: %v", err)
	}
	// The server's own database is off limits.
	own.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{}))
	mine := &mimirv1.Connection{Driver: "sqlite", Dsn: filepath.Join(svc.DataDir(), "mimir.db")}
	if _, err := own.datasets.TestQuery(ctx, connect.NewRequest(&mimirv1.TestQueryRequest{Connection: mine, Query: "SELECT email, password_hash FROM users"})); code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "own data") {
		t.Fatalf("own database: %v", err)
	}
	// Only admins connect databases.
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "e@example.com", Name: "E", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}}))
	e := signIn(t, srv, "e@example.com")
	if _, err := e.datasets.TestQuery(ctx, connect.NewRequest(&mimirv1.TestQueryRequest{Connection: conn, Query: query})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("editor TestQuery: %v", err)
	}
	if _, err := e.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "x", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Connection: conn, Query: query})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("editor query dataset: %v", err)
	}

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Shop fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Connection: conn, Query: query, Target: "kind"}))
	if err != nil {
		t.Fatal(err)
	}
	d := ds.Msg.GetDataset()
	if d.GetRows() != 200 || d.GetDriver() != "sqlite" || d.GetQuery() != query || d.GetRefreshTime() == nil || strings.Contains(fmt.Sprint(d), path) {
		t.Fatalf("dataset %v", d)
	}

	// Teach it, and keep learning daily.
	if _, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Sorter", Architecture: "table-mlp", DatasetId: d.GetId(), RepeatSeconds: 60})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("too often: %v", err)
	}
	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Sorter", Architecture: "table-mlp", DatasetId: d.GetId(), RepeatSeconds: 86400, Settings: &mimirv1.TrainingSettings{Epochs: 8, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	first := job.Msg.GetJob().GetId()
	runner.Tick(ctx)
	j1, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: first}))
	modelID := j1.Msg.GetJob().GetModelId()
	if j1.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || modelID == "" {
		t.Fatalf("first pass %v", j1.Msg.GetJob())
	}
	m1, _ := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: modelID}))
	if m1.Msg.GetModel().GetAccuracy() < 0.9 {
		t.Fatalf("model accuracy %v", m1.Msg.GetModel().GetAccuracy())
	}

	// Nothing is due yet.
	runner.Tick(ctx)
	if ls, _ := own.training.ListJobs(ctx, connect.NewRequest(&mimirv1.ListJobsRequest{})); len(ls.Msg.GetJobs()) != 1 {
		t.Fatalf("early pass: %d jobs", len(ls.Msg.GetJobs()))
	}

	// A day later, with new rows, the next pass fetches them and
	// continues from the model, keeping its id.
	addFruit(t, path, 100)
	runner.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	defer func() { runner.Now = time.Now }()
	runner.Tick(ctx)
	ls, _ := own.training.ListJobs(ctx, connect.NewRequest(&mimirv1.ListJobsRequest{}))
	if len(ls.Msg.GetJobs()) != 2 {
		t.Fatalf("after a day: %d jobs", len(ls.Msg.GetJobs()))
	}
	second := ls.Msg.GetJobs()[0]
	if second.GetParentId() != first || second.GetBaseModelId() != modelID || second.GetModelId() != modelID || second.GetRepeatSeconds() != 86400 || second.GetNote() == "" || second.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		t.Fatalf("second pass %v", second)
	}
	if ls.Msg.GetJobs()[1].GetRepeatSeconds() != 0 {
		t.Fatalf("first pass still repeats: %v", ls.Msg.GetJobs()[1])
	}
	dd, _ := own.datasets.GetDataset(ctx, connect.NewRequest(&mimirv1.GetDatasetRequest{Id: d.GetId()}))
	if dd.Msg.GetDataset().GetRows() != 300 {
		t.Fatalf("rows after refresh %d", dd.Msg.GetDataset().GetRows())
	}
	if ms, _ := own.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); len(ms.Msg.GetModels()) != 1 {
		t.Fatalf("models after second pass: %d", len(ms.Msg.GetModels()))
	}
	p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: modelID, Input: &mimirv1.Tensor{Values: []float32{-1, -1}}}))
	if err != nil || p.Msg.GetGuesses()[0].GetLabel() != "pear" {
		t.Fatalf("Predict after second pass = %v, %v", p, err)
	}

	// Turning it off stops further passes.
	if _, err := own.training.SetRepeat(ctx, connect.NewRequest(&mimirv1.SetRepeatRequest{Id: second.GetId()})); err != nil {
		t.Fatal(err)
	}
	runner.Now = func() time.Time { return time.Now().Add(50 * time.Hour) }
	runner.Tick(ctx)
	if ls, _ := own.training.ListJobs(ctx, connect.NewRequest(&mimirv1.ListJobsRequest{})); len(ls.Msg.GetJobs()) != 2 {
		t.Fatalf("after stopping: %d jobs", len(ls.Msg.GetJobs()))
	}

	// Refresh by hand; uploads have nothing to refresh.
	if r, err := own.datasets.RefreshDataset(ctx, connect.NewRequest(&mimirv1.RefreshDatasetRequest{Id: d.GetId()})); err != nil || r.Msg.GetDataset().GetRows() != 300 {
		t.Fatalf("RefreshDataset = %v, %v", r, err)
	}
	up, _ := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Up", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(50))}))
	if _, err := own.datasets.RefreshDataset(ctx, connect.NewRequest(&mimirv1.RefreshDatasetRequest{Id: up.Msg.GetDataset().GetId()})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("refresh an upload: %v", err)
	}
}
