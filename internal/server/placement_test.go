package server_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/store"
	builtin "github.com/gsoultan/mimir-server/internal/worker"
	"github.com/gsoultan/mimir-server/worker"
)

// joinWorker registers a worker and runs it until the test ends. It
// returns the worker's id and a func that stops it.
func joinWorker(t *testing.T, own clients, url, name string) (id, token string, stop func()) {
	t.Helper()
	created, err := own.workers.CreateWorker(context.Background(), connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: name}))
	if err != nil {
		t.Fatal(err)
	}
	id, token = created.Msg.GetWorker().GetId(), created.Msg.GetToken()
	stop = runWorker(t, url, token, t.TempDir())
	waitOnline(t, own, id, true)
	return id, token, stop
}

func runWorker(t *testing.T, url, token, dir string) func() {
	return runWorkerFor(t, url, token, dir, worker.TasksAll)
}

// runWorkerFor is runWorker for a worker that takes only some tasks.
func runWorkerFor(t *testing.T, url, token, dir string, tasks worker.Tasks) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx, worker.Config{Server: url, Token: token, Capacity: 2, Tasks: tasks, Version: "test", DataDir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, builtin.Builtin(t.TempDir()))
	}()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return func() { stop(); stop = func() {} }
}

func waitOnline(t *testing.T, own clients, id string, online bool) *mimirv1.Worker {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w, err := own.workers.GetWorker(context.Background(), connect.NewRequest(&mimirv1.GetWorkerRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		if w.Msg.GetWorker().GetOnline() == online {
			return w.Msg.GetWorker()
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker %s never became online=%v", id, online)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestPlacement places a training run and a model on one worker, and
// checks that work waits for that worker rather than going elsewhere.
func TestPlacement(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	svc.Local, runner.Local = false, false
	a, _, stopA := joinWorker(t, own, srv.URL, "alpha")
	b, _, stopB := joinWorker(t, own, srv.URL, "beta")

	choices, err := own.workers.ListWorkerChoices(ctx, connect.NewRequest(&mimirv1.ListWorkerChoicesRequest{}))
	if err != nil || len(choices.Msg.GetWorkers()) != 2 || !choices.Msg.GetWorkers()[0].GetOnline() || choices.Msg.GetWorkers()[0].GetAddress() != "" {
		t.Fatalf("ListWorkerChoices = %v, %v", choices, err)
	}

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	newJob := func(name, on string) string {
		t.Helper()
		j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: name, Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01}, WorkerId: on}))
		if err != nil {
			t.Fatal(err)
		}
		return j.Msg.GetJob().GetId()
	}
	getJob := func(id string) *mimirv1.Job {
		j, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		return j.Msg.GetJob()
	}
	if _, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "x", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), WorkerId: "01900000-0000-7000-8000-00000000dead"})); code(err) != connect.CodeNotFound {
		t.Fatalf("job on a missing worker: %v", err)
	}

	// A run placed on beta trains there, and its model answers there.
	jb := getJob(newJob("On beta", b))
	runner.Tick(ctx)
	jb = getJob(jb.GetId())
	if jb.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || jb.GetWorker() != "beta" || jb.GetRunOn() != b {
		t.Fatalf("job on beta: %v", jb)
	}
	model := jb.GetModelId()
	m, _ := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: model}))
	if m.Msg.GetModel().GetWorkerId() != b {
		t.Fatalf("model from beta runs on %q", m.Msg.GetModel().GetWorkerId())
	}
	ask := func() error {
		_, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: model, Input: &mimirv1.Tensor{Values: []float32{-1, -1}}}))
		return err
	}
	if err := ask(); err != nil {
		t.Fatal(err)
	}

	// Beta's dashboard shows it.
	d, err := own.workers.GetWorker(ctx, connect.NewRequest(&mimirv1.GetWorkerRequest{Id: b, Hours: 24}))
	if err != nil {
		t.Fatal(err)
	}
	dw := d.Msg.GetWorker()
	if dw.GetModels() != 1 || len(d.Msg.GetModels()) != 1 || len(d.Msg.GetRuns()) != 1 || dw.GetSecurity() != "local" || !strings.HasPrefix(dw.GetKeyFingerprint(), "SHA256:") || dw.GetStats() == nil || dw.GetConnectTime() == nil {
		t.Fatalf("beta's dashboard = %v", d.Msg)
	}
	if len(d.Msg.GetSamples()) == 0 || d.Msg.GetSamples()[0].GetCapacity() != 2 {
		t.Fatalf("beta's samples = %v", d.Msg.GetSamples())
	}
	if len(d.Msg.GetEvents()) == 0 || d.Msg.GetEvents()[0].GetName() != "worker.join" {
		t.Fatalf("beta's events = %v", d.Msg.GetEvents())
	}
	if _, err := own.workers.GetWorker(ctx, connect.NewRequest(&mimirv1.GetWorkerRequest{Id: b, Hours: 5})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("odd hours: %v", err)
	}

	// With beta gone, its model says so instead of moving to alpha, and a
	// run for beta waits without holding up one for anyone.
	stopB()
	waitOnline(t, own, b, false)
	if err := ask(); code(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("model on offline beta: %v", err)
	}
	waiting, anyone := newJob("For beta", b), newJob("For anyone", "")
	runner.Tick(ctx)
	if s := getJob(waiting).GetStatus(); s != mimirv1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("job for offline beta: %v", s)
	}
	if j := getJob(anyone); j.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || j.GetWorker() != "alpha" {
		t.Fatalf("job for anyone: %v", j)
	}
	ls, _ := own.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{}))
	for _, w := range ls.Msg.GetWorkers() {
		if w.GetId() == b && (w.GetWaiting() != 1 || w.GetModels() != 1) {
			t.Fatalf("beta in the list: %v", w)
		}
	}

	// Back to automatic, alpha answers.
	if _, err := own.models.SetModelWorker(ctx, connect.NewRequest(&mimirv1.SetModelWorkerRequest{Id: model})); err != nil {
		t.Fatal(err)
	}
	if err := ask(); err != nil {
		t.Fatalf("automatic: %v", err)
	}
	if _, err := own.models.SetModelWorker(ctx, connect.NewRequest(&mimirv1.SetModelWorkerRequest{Id: model, WorkerId: a})); err != nil {
		t.Fatal(err)
	}
	if err := ask(); err != nil {
		t.Fatalf("on alpha: %v", err)
	}

	// Removing beta puts its waiting run back on anyone.
	if _, err := own.workers.DeleteWorker(ctx, connect.NewRequest(&mimirv1.DeleteWorkerRequest{Id: b})); err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	if j := getJob(waiting); j.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || j.GetRunOn() != "" {
		t.Fatalf("beta's run after removing beta: %v", j)
	}

	// A run whose worker goes away mid-way goes back in the queue.
	slow, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Slow", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 400, BatchSize: 4}}))
	if err != nil {
		t.Fatal(err)
	}
	ticked := make(chan struct{})
	go func() { runner.Tick(ctx); close(ticked) }()
	for getJob(slow.Msg.GetJob().GetId()).GetStatus() != mimirv1.JobStatus_JOB_STATUS_RUNNING {
		time.Sleep(20 * time.Millisecond)
	}
	stopA()
	<-ticked
	if j := getJob(slow.Msg.GetJob().GetId()); j.GetStatus() != mimirv1.JobStatus_JOB_STATUS_QUEUED || j.GetError() != "" {
		t.Fatalf("run after its worker left: %v", j)
	}

	// Editors can place models but not see the worker dashboards.
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "e@example.com", Name: "E", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}}))
	e := signIn(t, srv, "e@example.com")
	if _, err := e.workers.ListWorkerChoices(ctx, connect.NewRequest(&mimirv1.ListWorkerChoicesRequest{})); err != nil {
		t.Fatalf("editor choices: %v", err)
	}
	if _, err := e.workers.GetWorker(ctx, connect.NewRequest(&mimirv1.GetWorkerRequest{Id: a})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("editor dashboard: %v", err)
	}
}

// TestWorkerLinkSecurity checks a token alone isn't enough once a worker
// has joined, and that a new token lets a rebuilt worker back in.
func TestWorkerLinkSecurity(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	id, token, stop := joinWorker(t, own, srv.URL, "lab")
	stop()
	waitOnline(t, own, id, false)

	h2c := new(http.Protocols)
	h2c.SetUnencryptedHTTP2(true)
	raw := mimirv1connect.NewWorkerServiceClient(&http.Client{Transport: &http.Transport{Protocols: h2c}}, srv.URL, connect.WithGRPC())
	// try links with a key and signs the challenge with signer.
	try := func(tok string, key ed25519.PublicKey, signer ed25519.PrivateKey) error {
		s := raw.Link(ctx)
		defer s.CloseResponse()
		defer s.CloseRequest()
		s.RequestHeader().Set("Authorization", "Bearer "+tok)
		s.Send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Hello{Hello: &mimirv1.Hello{Architectures: []string{"table-mlp"}, PublicKey: key}}})
		msg, err := s.Receive()
		if err != nil {
			return err
		}
		c := msg.GetChallenge()
		if c == nil {
			t.Fatalf("no challenge: %v", msg)
		}
		s.Send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Proof{Proof: &mimirv1.Proof{Signature: ed25519.Sign(signer, append([]byte(cluster.ProofContext), c.Nonce...))}}})
		_, err = s.Receive()
		return err
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)

	// The right token from another machine is refused, signed or not.
	if err := try(token, pub, other); code(err) != connect.CodePermissionDenied {
		t.Fatalf("bad signature: %v", err)
	}
	if err := try(token, pub, priv); code(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "another machine") {
		t.Fatalf("stolen token: %v", err)
	}
	// A hello with no key at all is refused too.
	{
		s := raw.Link(ctx)
		s.RequestHeader().Set("Authorization", "Bearer "+token)
		s.Send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Hello{Hello: &mimirv1.Hello{Architectures: []string{"table-mlp"}}}})
		if _, err := s.Receive(); code(err) != connect.CodePermissionDenied {
			t.Fatalf("no key: %v", err)
		}
		s.CloseRequest()
		s.CloseResponse()
	}
	ev, _ := own.workers.GetWorker(ctx, connect.NewRequest(&mimirv1.GetWorkerRequest{Id: id}))
	if ev.Msg.GetEvents()[0].GetName() != "worker.refused" {
		t.Fatalf("events after a refusal: %v", ev.Msg.GetEvents())
	}

	// A new token cuts off the old one and lets a fresh key join.
	rep, err := own.workers.ReplaceWorkerToken(ctx, connect.NewRequest(&mimirv1.ReplaceWorkerTokenRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	if err := try(token, pub, priv); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("old token: %v", err)
	}
	stop = runWorker(t, srv.URL, rep.Msg.GetToken(), t.TempDir())
	w := waitOnline(t, own, id, true)
	if w.GetKeyFingerprint() == "" || w.GetKeyFingerprint() == api.Fingerprint(pub) {
		t.Fatalf("key after a new token: %q", w.GetKeyFingerprint())
	}
	stop()
}

// TestWorkerPinnedTLS joins over TLS with a certificate nobody vouches
// for: the worker trusts it only because the token carries its key's
// fingerprint, and refuses a console with another key.
func TestWorkerPinnedTLS(t *testing.T) {
	srv := startServer(t, true)
	ctx := context.Background()
	own := owner(t, srv)
	cert := srv.TLS.Certificates[0]
	cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	svc.ConsolePin = api.PinOf(&cert)
	ls, _ := own.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{}))
	if !ls.Msg.GetSecureJoin() {
		t.Fatal("secure_join is off with a pin")
	}

	id, token, stop := joinWorker(t, own, srv.URL, "remote")
	if !strings.HasSuffix(token, "."+svc.ConsolePin) {
		t.Fatalf("join token %q has no pin", token)
	}
	w := waitOnline(t, own, id, true)
	if w.GetSecurity() != "tls" || !strings.HasPrefix(w.GetTlsVersion(), "TLS 1.") {
		t.Fatalf("link security = %q %q", w.GetSecurity(), w.GetTlsVersion())
	}
	stop()
	waitOnline(t, own, id, false)

	// The same token with another console's fingerprint never links.
	secret, _, _ := strings.Cut(token, ".")
	stop = runWorker(t, srv.URL, secret+".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", t.TempDir())
	time.Sleep(time.Second)
	if waitOnline(t, own, id, false).GetOnline() {
		t.Fatal("linked to a console with the wrong key")
	}
	stop()

	// And a token with a pin won't go over plain http.
	err := worker.Run(ctx, worker.Config{Server: "http://127.0.0.1:1", Token: token, DataDir: t.TempDir()}, builtin.Builtin(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("pinned token over http: %v", err)
	}
	err = worker.Run(ctx, worker.Config{Server: "http://10.1.2.3:8080", Token: secret, DataDir: t.TempDir()}, builtin.Builtin(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "isn't encrypted") {
		t.Fatalf("plain http to another machine: %v", err)
	}
}

// TestWorkerTasks keeps training and serving apart: a worker started with
// -tasks train gets the runs and none of the predictions, one with
// -tasks serve the other way round, and a model trained on the training
// worker answers on the serving one.
func TestWorkerTasks(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	svc.Local, runner.Local = false, false

	join := func(name string, tasks worker.Tasks) string {
		t.Helper()
		created, err := own.workers.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: name}))
		if err != nil {
			t.Fatal(err)
		}
		runWorkerFor(t, srv.URL, created.Msg.GetToken(), t.TempDir(), tasks)
		return created.Msg.GetWorker().GetId()
	}
	trainer := join("gpu-box", worker.TasksTrain)
	server := join("api-box", worker.TasksServe)
	tw, sw := waitOnline(t, own, trainer, true), waitOnline(t, own, server, true)
	if !tw.GetTrains() || !tw.GetTrainingOnly() {
		t.Fatalf("training worker reports %v", tw)
	}
	if sw.GetTrains() || sw.GetTrainingOnly() {
		t.Fatalf("serving worker reports %v", sw)
	}

	// A model can't be placed on the training worker.
	m := createModel(t, own, "Digits", "mnist-mlp")
	if _, err := own.models.SetModelWorker(ctx, connect.NewRequest(&mimirv1.SetModelWorkerRequest{Id: m.GetId(), WorkerId: trainer})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("placing a model on the training worker: %v", err)
	}

	// An automatic run goes to the training worker, and the model it
	// makes is left on automatic, so the serving worker answers it.
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	for _, on := range []string{"", trainer} {
		j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit sorter", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01}, WorkerId: on}))
		if err != nil {
			t.Fatal(err)
		}
		runner.Tick(ctx)
		got, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
		jb := got.Msg.GetJob()
		if jb.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || jb.GetWorker() != "gpu-box" {
			t.Fatalf("run placed on %q: %v", on, jb)
		}
		trained, _ := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: jb.GetModelId()}))
		if trained.Msg.GetModel().GetWorkerId() != "" {
			t.Fatalf("model trained on the training worker runs on %q, want automatic", trained.Msg.GetModel().GetWorkerId())
		}
		p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: jb.GetModelId(), Input: &mimirv1.Tensor{Values: []float32{-1, -1}}}))
		if err != nil || p.Msg.GetGuesses()[0].GetLabel() != "pear" {
			t.Fatalf("Predict via the serving worker = %v, %v", p, err)
		}
	}
	// Both runs show on the training worker's page and none on the
	// serving worker's.
	for id, want := range map[string]int{trainer: 2, server: 0} {
		d, err := own.workers.GetWorker(ctx, connect.NewRequest(&mimirv1.GetWorkerRequest{Id: id, Hours: 1}))
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Msg.GetRuns()) != want {
			t.Fatalf("worker %s trained %v, want %d runs", d.Msg.GetWorker().GetName(), d.Msg.GetRuns(), want)
		}
	}

	// A run pinned to the serving worker waits rather than running there.
	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Nope", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 1}, WorkerId: server}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	if got, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()})); got.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("run pinned to the serving worker: %v", got.Msg.GetJob())
	}

	// From the console, a worker started for both can be told what to
	// take, at once. Told to teach only, the model placed on it goes back
	// to automatic; told to answer only, the run waiting for it does too.
	both := join("both-box", worker.TasksAll)
	bw := waitOnline(t, own, both, true)
	if !bw.GetTrains() || bw.GetTrainingOnly() || bw.GetTasks() != "" {
		t.Fatalf("worker for both reports %v", bw)
	}
	if _, err := own.models.SetModelWorker(ctx, connect.NewRequest(&mimirv1.SetModelWorkerRequest{Id: m.GetId(), WorkerId: both})); err != nil {
		t.Fatal(err)
	}
	if _, err := own.workers.SetWorkerTasks(ctx, connect.NewRequest(&mimirv1.SetWorkerTasksRequest{Id: both, Tasks: "sometimes"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("odd tasks: %v", err)
	}
	set := func(tasks string) *mimirv1.Worker {
		t.Helper()
		res, err := own.workers.SetWorkerTasks(ctx, connect.NewRequest(&mimirv1.SetWorkerTasksRequest{Id: both, Tasks: tasks}))
		if err != nil {
			t.Fatalf("SetWorkerTasks(%q): %v", tasks, err)
		}
		return res.Msg.GetWorker()
	}
	if w := set("train"); !w.GetTrainingOnly() || !w.GetTrains() || w.GetTasks() != "train" {
		t.Fatalf("told to train only: %v", w)
	}
	if got, _ := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: m.GetId()})); got.Msg.GetModel().GetWorkerId() != "" {
		t.Fatalf("model placed on a worker told to train only still runs on %q", got.Msg.GetModel().GetWorkerId())
	}
	if _, err := own.models.SetModelWorker(ctx, connect.NewRequest(&mimirv1.SetModelWorkerRequest{Id: m.GetId(), WorkerId: both})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("placing a model on a worker told to train only: %v", err)
	}
	pinned, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "For both", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 1}, WorkerId: both}))
	if err != nil {
		t.Fatal(err)
	}
	if w := set("serve"); w.GetTrainingOnly() || w.GetTrains() || w.GetTasks() != "serve" {
		t.Fatalf("told to serve only: %v", w)
	}
	if got, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: pinned.Msg.GetJob().GetId()})); got.Msg.GetJob().GetRunOn() != "" {
		t.Fatalf("run pinned to a worker told to serve only still waits for %q", got.Msg.GetJob().GetRunOn())
	}
	if w := set(""); !w.GetTrains() || w.GetTrainingOnly() || w.GetTasks() != "" {
		t.Fatalf("back to its own choice: %v", w)
	}
	if ls, _ := own.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{})); len(ls.Msg.GetWorkers()) != 3 {
		t.Fatalf("ListWorkers = %v", ls)
	}
}

// TestTrainHereSetting turns training on the console itself off from
// Settings: a run nobody else can take waits, and runs again when it is
// turned back on.
func TestTrainHereSetting(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	runner.TrainHere = svc.TrainsHere
	t.Cleanup(func() { runner.TrainHere = nil })

	got, err := own.settings.GetSettings(ctx, connect.NewRequest(&mimirv1.GetSettingsRequest{}))
	if err != nil || !got.Msg.GetTraining().GetTrainHere() || !got.Msg.GetTraining().GetCanTrainHere() {
		t.Fatalf("GetSettings training = %v, %v", got.Msg.GetTraining(), err)
	}
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := own.settings.UpdateTraining(ctx, connect.NewRequest(&mimirv1.UpdateTrainingRequest{TrainHere: false})); err != nil {
		t.Fatal(err)
	}
	if svc.TrainsHere() {
		t.Fatal("the console still trains here after the setting was turned off")
	}
	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Waits", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 2}}))
	if err != nil {
		t.Fatal(err)
	}
	job := func() *mimirv1.Job {
		res, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg.GetJob()
	}
	runner.Tick(ctx)
	if s := job().GetStatus(); s != mimirv1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("with training here off and no worker, the run is %v, want queued", s)
	}
	res, err := own.settings.UpdateTraining(ctx, connect.NewRequest(&mimirv1.UpdateTrainingRequest{TrainHere: true}))
	if err != nil || !res.Msg.GetTraining().GetTrainHere() {
		t.Fatalf("UpdateTraining on = %v, %v", res, err)
	}
	runner.Tick(ctx)
	if jb := job(); jb.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || jb.GetWorker() != "" {
		t.Fatalf("with training here back on, the run is %v", jb)
	}
	events, _ := own.audit.ListAuditEvents(ctx, connect.NewRequest(&mimirv1.ListAuditEventsRequest{}))
	found := false
	for _, e := range events.Msg.GetEvents() {
		found = found || e.GetAction() == "settings.training"
	}
	if !found {
		t.Fatal("no settings.training audit event")
	}
}

// loadedOn waits until the worker's link holds version ver of the model,
// which Warm brings about without any prediction asking for it.
func loadedOn(t *testing.T, workerID, modelID, ver string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, st := range svc.Pool.Statuses() {
			if st.Worker.ID == workerID && st.Loaded[modelID] == ver {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker %s never got version %s of model %s", workerID, ver, modelID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWarmHandoff has a training-only worker train a model and checks
// that the serving worker gets the weights before anyone asks, and that
// predictions keep succeeding while the model is replaced by a new
// version underneath them.
func TestWarmHandoff(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	svc.Local, runner.Local = false, false

	join := func(name string, tasks worker.Tasks) string {
		t.Helper()
		created, err := own.workers.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: name}))
		if err != nil {
			t.Fatal(err)
		}
		runWorkerFor(t, srv.URL, created.Msg.GetToken(), t.TempDir(), tasks)
		waitOnline(t, own, created.Msg.GetWorker().GetId(), true)
		return created.Msg.GetWorker().GetId()
	}
	join("trainer", worker.TasksTrain)
	server := join("server", worker.TasksServe)

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	train := func(name string) *mimirv1.Model {
		t.Helper()
		j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: name, Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01}}))
		if err != nil {
			t.Fatal(err)
		}
		runner.Tick(ctx)
		got, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
		if got.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
			t.Fatalf("run %s: %v", name, got.Msg.GetJob())
		}
		m, err := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: got.Msg.GetJob().GetModelId()}))
		if err != nil {
			t.Fatal(err)
		}
		return m.Msg.GetModel()
	}

	// The serving worker holds the model before anyone asks for it.
	m := train("Fruit sorter")
	loadedOn(t, server, m.GetId(), m.GetVersion())

	// Predictions keep coming while the model is replaced, twice, by
	// the weights of a fresh run: none fails, and the serving worker
	// ends up with the newest version.
	st2, err := store.Open(ctx, dbURL, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var (
		stop   atomic.Bool
		asked  atomic.Int64
		failed atomic.Value // the first error
		wg     sync.WaitGroup
	)
	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				_, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: []float32{-1, -1}}}))
				asked.Add(1)
				if err != nil {
					failed.CompareAndSwap(nil, err)
					return
				}
			}
		})
	}
	ver := m.GetVersion()
	for i := range 2 {
		fresh := train("Fresh " + string(rune('A'+i)))
		w, err := st2.Weights(fresh.GetId())
		if err != nil {
			t.Fatal(err)
		}
		cur, err := st2.Get(ctx, m.GetId())
		if err != nil {
			t.Fatal(err)
		}
		next := cur
		next.InputShape, next.Labels, next.Features, next.Accuracy = ints(fresh.GetInputShape()), fresh.GetLabels(), fresh.GetFeatures(), fresh.GetAccuracy()
		if err := st2.Replace(ctx, m.GetId(), next, w); err != nil {
			t.Fatal(err)
		}
		replaced, err := st2.Get(ctx, m.GetId())
		if err != nil {
			t.Fatal(err)
		}
		if replaced.WeightsVersion() == ver {
			t.Fatal("the replacement has the same version")
		}
		ver = replaced.WeightsVersion()
		svc.Warm(ctx, replaced) // what the console does when a run replaces a model
		loadedOn(t, server, m.GetId(), ver)
		time.Sleep(100 * time.Millisecond) // a few predictions on the new version
	}
	stop.Store(true)
	wg.Wait()
	if err := failed.Load(); err != nil {
		t.Fatalf("a prediction failed during the switch: %v", err)
	}
	if asked.Load() < 20 {
		t.Fatalf("only %d predictions were made; the switch should not have held them up", asked.Load())
	}
}

func ints(v []int64) []int {
	out := make([]int, len(v))
	for i, x := range v {
		out[i] = int(x)
	}
	return out
}
