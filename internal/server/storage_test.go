package server_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// s3 is a tiny S3-compatible storage: one bucket, path-style.
type s3 struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (s *s3) put(key string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[key] = b
}

func (s *s3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIDEXAMPLE/") {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<Error><Code>InvalidAccessKeyId</Code></Error>")
		return
	}
	key, ok := strings.CutPrefix(r.URL.Path, "/training/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "<Error><Code>NoSuchBucket</Code></Error>")
		return
	}
	if key == "" {
		type content struct {
			Key  string
			Size int
		}
		var out struct {
			XMLName  xml.Name `xml:"ListBucketResult"`
			Contents []content
		}
		for k, b := range s.files {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				out.Contents = append(out.Contents, content{k, len(b)})
			}
		}
		slices.SortFunc(out.Contents, func(a, b content) int { return strings.Compare(a.Key, b.Key) })
		if m := r.URL.Query().Get("max-keys"); m == "1" && len(out.Contents) > 1 {
			out.Contents = out.Contents[:1]
		}
		xml.NewEncoder(w).Encode(out)
		return
	}
	b, ok := s.files[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
		return
	}
	w.Write(b)
}

func TestTeachFromStorageAndKeepLearning(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	store := &s3{files: map[string][]byte{}}
	rows := strings.Split(strings.TrimSpace(blobs(200)), "\n")
	header := rows[0] + "\n"
	store.put("fruit/2026-01.csv", []byte(header+strings.Join(rows[1:101], "\n")+"\n"))
	store.put("fruit/2026-02.csv", []byte(header+strings.Join(rows[101:], "\n")+"\n"))
	for i := range 12 {
		store.put(fmt.Sprintf("pets/cat/%d.png", i), photoPNG())
		store.put(fmt.Sprintf("pets/dog/%d.png", i), photoPNG())
	}
	s3srv := httptest.NewServer(store)
	defer s3srv.Close()
	bkt := &mimirv1.Bucket{Provider: "other", Endpoint: s3srv.URL, Bucket: "training", AccessKeyId: "AKIDEXAMPLE", SecretAccessKey: "very-secret"}

	tb, err := own.datasets.TestBucket(ctx, connect.NewRequest(&mimirv1.TestBucketRequest{Bucket: bkt, Path: "fruit/", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE}))
	if err != nil || tb.Msg.GetFiles() != 2 || tb.Msg.GetRows() != 200 || strings.Join(tb.Msg.GetColumns(), ",") != strings.TrimSpace(header) || len(tb.Msg.GetSample()) != 5 {
		t.Fatalf("TestBucket = %v, %v", tb, err)
	}
	wrong := &mimirv1.Bucket{Provider: "other", Endpoint: s3srv.URL, Bucket: "training", AccessKeyId: "nope", SecretAccessKey: "x"}
	if _, err := own.datasets.TestBucket(ctx, connect.NewRequest(&mimirv1.TestBucketRequest{Bucket: wrong, Path: "fruit/"})); code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "access key") {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := own.datasets.TestBucket(ctx, connect.NewRequest(&mimirv1.TestBucketRequest{Bucket: &mimirv1.Bucket{Provider: "r2", Bucket: "b", AccessKeyId: "k", SecretAccessKey: "s"}})); code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "account ID") {
		t.Fatalf("r2 without account: %v", err)
	}

	// Only admins connect storage.
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "e@example.com", Name: "E", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}}))
	e := signIn(t, srv, "e@example.com")
	if _, err := e.datasets.TestBucket(ctx, connect.NewRequest(&mimirv1.TestBucketRequest{Bucket: bkt, Path: "fruit/"})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("editor TestBucket: %v", err)
	}
	if _, err := e.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "x", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Bucket: bkt, Path: "fruit/"})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("editor storage dataset: %v", err)
	}

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit from storage", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Bucket: bkt, Path: "fruit", Target: "kind"}))
	if err != nil {
		t.Fatal(err)
	}
	d := ds.Msg.GetDataset()
	if d.GetRows() != 200 || d.GetStorage().GetBucket() != "training" || d.GetStorage().GetPath() != "fruit" || d.GetQuery() != "" || d.GetRefreshTime() == nil || strings.Contains(fmt.Sprint(d), "very-secret") {
		t.Fatalf("dataset %v", d)
	}

	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Sorter", Architecture: "table-mlp", DatasetId: d.GetId(), RepeatSeconds: 86400, Settings: &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	if j, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: job.Msg.GetJob().GetId()})); j.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		t.Fatalf("first pass %v", j.Msg.GetJob())
	}

	// A new file lands in the folder; the next pass reads it too.
	more := strings.Split(strings.TrimSpace(blobs(50)), "\n")
	store.put("fruit/2026-03.csv", []byte(header+strings.Join(more[1:], "\n")+"\n"))
	runner.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	defer func() { runner.Now = time.Now }()
	runner.Tick(ctx)
	dd, _ := own.datasets.GetDataset(ctx, connect.NewRequest(&mimirv1.GetDatasetRequest{Id: d.GetId()}))
	if dd.Msg.GetDataset().GetRows() != 250 {
		t.Fatalf("rows after refresh %d", dd.Msg.GetDataset().GetRows())
	}
	if _, err := own.datasets.RefreshDataset(ctx, connect.NewRequest(&mimirv1.RefreshDatasetRequest{Id: d.GetId()})); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// A folder of photos, one folder per answer, is packed like an upload.
	ps, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Pets", Kind: mimirv1.DatasetKind_DATASET_KIND_PHOTOS, Bucket: bkt, Path: "pets/"}))
	if err != nil {
		t.Fatal(err)
	}
	if p := ps.Msg.GetDataset(); p.GetRows() != 24 || !slices.Equal(p.GetLabels(), []string{"cat", "dog"}) || p.GetStorage() != nil {
		t.Fatalf("photos %v", p)
	}
}
