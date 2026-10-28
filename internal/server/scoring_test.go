package server_test

import (
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// runScorings runs the scoring runner until the test ends.
func runScorings(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunScorings(ctx, 10*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// waitScoring waits for a scoring to be over, whichever way.
func waitScoring(t *testing.T, c mimirv1connect.ScoringServiceClient, id string) *mimirv1.GetScoringResponse {
	t.Helper()
	for range 500 {
		got, err := c.GetScoring(context.Background(), connect.NewRequest(&mimirv1.GetScoringRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Msg.GetScoring().GetStatus() >= mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
			return got.Msg
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("scoring %s never finished", id)
	return nil
}

// trainFruit teaches a table model on blobs and returns its id.
func trainFruit(t *testing.T, c clients) (modelID, datasetID string) {
	t.Helper()
	ctx := context.Background()
	ds, err := c.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := c.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 6, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	job, err := c.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if err != nil || job.Msg.GetJob().GetModelId() == "" {
		t.Fatalf("job %v, %v", job, err)
	}
	return job.Msg.GetJob().GetModelId(), ds.Msg.GetDataset().GetId()
}

// download fetches a scoring's results as the signed-in c would.
func download(t *testing.T, srv interface{ Client() *http.Client }, url, cookie string) (int, [][]string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, res.Body)
		return res.StatusCode, nil
	}
	rows, err := csv.NewReader(res.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, rows
}

func TestScoreAWholeTable(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	sc := mimirv1connect.NewScoringServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))
	modelID, datasetID := trainFruit(t, own)
	runScorings(t)

	// An upload with no answers: every row is answered, and a row the
	// model can't read says why rather than stopping the rest.
	upload := "id,width,height\n1,1.1,0.9\n2,-1.2,-0.8\n3,oops,1\n4,0.9,1.2\n"
	c, err := sc.CreateScoring(ctx, connect.NewRequest(&mimirv1.CreateScoringRequest{ModelId: modelID, Csv: []byte(upload)}))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Msg.GetScoring()
	if s.GetName() != "Fruit on the uploaded file" || s.GetSource() != "upload" || s.GetStatus() != mimirv1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("created %v", s)
	}
	got := waitScoring(t, sc, s.GetId())
	s = got.GetScoring()
	if s.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || s.GetRows() != 4 || s.GetDone() != 4 || s.GetFailed() != 1 || s.GetRuns() != 1 {
		t.Fatalf("finished %v", s)
	}
	want := []string{"id", "width", "height", "predicted", "confidence", "problem"}
	if !slices.Equal(s.GetColumns(), want) || len(got.GetSample()) != 4 || s.GetResultBytes() == 0 {
		t.Fatalf("columns %v, sample %v", s.GetColumns(), got.GetSample())
	}
	status, rows := download(t, srv, srv.URL+"/api/scorings/"+s.GetId()+"/results", own.jar.cookie)
	if status != http.StatusOK || len(rows) != 5 || !slices.Equal(rows[0], want) {
		t.Fatalf("download %d: %v", status, rows)
	}
	if rows[1][3] != "apple" || rows[2][3] != "pear" || rows[4][3] != "apple" || rows[1][4] == "" {
		t.Fatalf("answers %v", rows)
	}
	if rows[3][3] != "" || !strings.Contains(rows[3][5], "needs a number") {
		t.Fatalf("bad row %v", rows[3])
	}

	// The results need a signed-in person who may see the model.
	if status, _ := download(t, srv, srv.URL+"/api/scorings/"+s.GetId()+"/results", ""); status != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", status)
	}

	// Rows that hold none of the model's columns are refused at once.
	if _, err := sc.CreateScoring(ctx, connect.NewRequest(&mimirv1.CreateScoringRequest{ModelId: modelID, Csv: []byte("a,b\n1,2\n")})); code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "width") {
		t.Fatalf("no columns: %v", err)
	}
	// So is an upload on a schedule: its rows never change.
	if _, err := sc.CreateScoring(ctx, connect.NewRequest(&mimirv1.CreateScoringRequest{ModelId: modelID, Csv: []byte(upload), RepeatSeconds: 3600})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("upload on a schedule: %v", err)
	}
	// And two sources at once.
	if _, err := sc.CreateScoring(ctx, connect.NewRequest(&mimirv1.CreateScoringRequest{ModelId: modelID, Csv: []byte(upload), DatasetId: datasetID})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("two sources: %v", err)
	}

	// A dataset's rows: its answer column comes along untouched.
	c, err = sc.CreateScoring(ctx, connect.NewRequest(&mimirv1.CreateScoringRequest{ModelId: modelID, DatasetId: datasetID, Name: "Check the shop"}))
	if err != nil {
		t.Fatal(err)
	}
	got = waitScoring(t, sc, c.Msg.GetScoring().GetId())
	if s := got.GetScoring(); s.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || s.GetDone() != 200 || s.GetFailed() != 0 || s.GetSource() != "dataset" || s.GetSourceDetail() != "Fruit" {
		t.Fatalf("dataset scoring %v", s)
	}
	right := 0
	for _, r := range got.GetSample() {
		if r.GetValues()[2] == r.GetValues()[3] {
			right++
		}
	}
	if right < 9 {
		t.Fatalf("only %d of 10 right: %v", right, got.GetSample())
	}

	// A database query, scored again on a schedule and on demand, reads
	// its rows afresh each time.
	path := fruitDB(t, 50)
	conn := &mimirv1.Connection{Driver: "sqlite", Dsn: path}
	c, err = sc.CreateScoring(ctx, connect.NewRequest(&mimirv1.CreateScoringRequest{ModelId: modelID, Connection: conn, Query: "SELECT width, height FROM fruit", RepeatSeconds: 86400}))
	if err != nil {
		t.Fatal(err)
	}
	id := c.Msg.GetScoring().GetId()
	got = waitScoring(t, sc, id)
	if s := got.GetScoring(); s.GetDone() != 50 || s.GetSource() != "database" || s.GetRepeatSeconds() != 86400 || s.GetNextRunTime() == nil || strings.Contains(s.String(), path) {
		t.Fatalf("query scoring %v", s)
	}
	addFruit(t, path, 30)
	if _, err := sc.RunScoring(ctx, connect.NewRequest(&mimirv1.RunScoringRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if s := waitScoring(t, sc, id).GetScoring(); s.GetDone() != 80 || s.GetRuns() != 2 {
		t.Fatalf("second run %v", s)
	}
	if _, err := sc.SetScoringRepeat(ctx, connect.NewRequest(&mimirv1.SetScoringRepeatRequest{Id: id, RepeatSeconds: 60})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("too often: %v", err)
	}
	r, err := sc.SetScoringRepeat(ctx, connect.NewRequest(&mimirv1.SetScoringRepeatRequest{Id: id}))
	if err != nil || r.Msg.GetScoring().GetRepeatSeconds() != 0 || r.Msg.GetScoring().GetNextRunTime() != nil {
		t.Fatalf("stop repeating: %v, %v", r, err)
	}

	// The list shows them all, newest first, and a model's own.
	l, err := sc.ListScorings(ctx, connect.NewRequest(&mimirv1.ListScoringsRequest{ModelId: modelID}))
	if err != nil || len(l.Msg.GetScorings()) != 3 || l.Msg.GetScorings()[0].GetId() != id {
		t.Fatalf("list %v, %v", l, err)
	}

	// Deleting a scoring removes its results.
	if _, err := sc.DeleteScoring(ctx, connect.NewRequest(&mimirv1.DeleteScoringRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if status, _ := download(t, srv, srv.URL+"/api/scorings/"+id+"/results", own.jar.cookie); status != http.StatusNotFound {
		t.Fatalf("deleted: %d", status)
	}

	// Deleting the model deletes its scorings.
	if _, err := own.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: modelID})); err != nil {
		t.Fatal(err)
	}
	if l, err := sc.ListScorings(ctx, connect.NewRequest(&mimirv1.ListScoringsRequest{})); err != nil || len(l.Msg.GetScorings()) != 0 {
		t.Fatalf("after the model went: %v, %v", l, err)
	}
}

func TestScoringOnlyForTableModels(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	sc := mimirv1connect.NewScoringServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))
	m := createModel(t, own, "Digits", "mnist-mlp")
	_, err := sc.CreateScoring(ctx, connect.NewRequest(&mimirv1.CreateScoringRequest{ModelId: m.GetId(), Csv: []byte("a,b\n1,2\n")}))
	if code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "doesn't read table rows") {
		t.Fatalf("photo model: %v", err)
	}
}
