package server_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/gpucloud"
	builtin "github.com/gsoultan/mimir-server/internal/worker"
	"github.com/gsoultan/mimir-server/worker"
)

// fakeCloud runs a "machine" as a worker in this process, with the token
// and key the console gives it, joined to the test server.
type fakeCloud struct {
	t      *testing.T
	server string

	mu      sync.Mutex
	started []gpucloud.Machine
	done    map[string]chan error
	stopped map[string]context.CancelFunc
}

func (f *fakeCloud) Start(_ context.Context, c gpucloud.Config, m gpucloud.Machine) (string, error) {
	key, err := worker.ParseKey(m.Secrets["MIMIR_WORKER_KEY"])
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "sb-" + string(rune('a'+len(f.started)))
	f.started = append(f.started, m)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	f.done[id], f.stopped[id] = done, cancel
	go func() {
		done <- worker.Run(ctx, worker.Config{Server: f.server, Token: m.Secrets["MIMIR_WORKER_TOKEN"], Key: key, Tasks: worker.TasksTrain,
			Capacity: c.Capacity, ExitWhenIdle: time.Second, Version: "test", DataDir: f.t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, builtin.Builtin(f.t.TempDir()))
	}()
	return id, nil
}

func (f *fakeCloud) Exited(_ context.Context, _ gpucloud.Config, id string) (bool, int, error) {
	f.mu.Lock()
	done := f.done[id]
	f.mu.Unlock()
	select {
	case err := <-done:
		code := 0
		if err != nil {
			code = 1
		}
		close(done)
		return true, code, nil
	default:
		return false, 0, nil
	}
}

func (f *fakeCloud) Stop(_ context.Context, _ gpucloud.Config, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped[id]()
	return nil
}

func (f *fakeCloud) Find(context.Context, gpucloud.Config, string) (string, error) { return "", nil }

// TestCloudGPU sets up renting a GPU, has a waiting run start one, which
// joins as the GPU's worker with the key the console made for it, trains
// the run and stops itself once idle.
func TestCloudGPU(t *testing.T) {
	fake := &fakeCloud{t: t, done: map[string]chan error{}, stopped: map[string]context.CancelFunc{}}
	var gpu *gpucloud.Launcher
	srv := startServerWith(t, false, func(s *api.Service) {
		gpu = gpucloud.NewLauncher(map[string]gpucloud.Cloud{gpucloud.ProviderModal: fake, gpucloud.ProviderRunPod: fake}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		gpu.Wanted = s.CloudGPUWanted
		gpu.Online = s.Pool.Online
		gpu.ServesProgram = true
		s.GPU = gpu
	})
	fake.server = srv.URL
	ctx := context.Background()
	own := owner(t, srv)

	get := func() *mimirv1.CloudGpu {
		t.Helper()
		r, err := own.workers.GetCloudGpu(ctx, connect.NewRequest(&mimirv1.GetCloudGpuRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.GetCloudGpu()
	}
	if g := get(); g.GetEnabled() || g.GetState() != gpucloud.StateOff || g.GetProvider() != "modal" || g.GetGpu() != "L4" || g.GetIdleMinutes() != 5 {
		t.Fatalf("before setup: %v", g)
	}
	if r, _ := own.workers.GetCloudGpu(ctx, connect.NewRequest(&mimirv1.GetCloudGpuRequest{})); len(r.Msg.GetProviders()) != 6 || r.Msg.GetProviders()[1].GetName() != "AWS EC2" || len(r.Msg.GetProviders()[1].GetGpus()) == 0 {
		t.Fatalf("providers: %v", r.Msg.GetProviders())
	}

	// Mistakes are refused in words.
	for _, bad := range []*mimirv1.UpdateCloudGpuRequest{
		{Enabled: true, Gpu: "L4", ConsoleUrl: "http://mimir.example.com"},
		{Enabled: true, Gpu: "L4", ConsoleUrl: "https://localhost:8080"},
		{Enabled: true, Gpu: "V100", ConsoleUrl: "https://mimir.example.com"},
		{Enabled: true, Gpu: "L4", ConsoleUrl: "https://mimir.example.com", TokenId: "ak-x"},
		{Enabled: true, Gpu: "L4", ConsoleUrl: "https://mimir.example.com", MaxHours: 48},
		{Enabled: true, Provider: "runpod", Gpu: "NVIDIA L4", ConsoleUrl: "https://mimir.example.com"},
		{Enabled: true, Provider: "azure", Gpu: "L4", ConsoleUrl: "https://mimir.example.com"},
	} {
		if _, err := own.workers.UpdateCloudGpu(ctx, connect.NewRequest(bad)); code(err) != connect.CodeInvalidArgument {
			t.Fatalf("UpdateCloudGpu(%v) = %v", bad, err)
		}
	}

	up, err := own.workers.UpdateCloudGpu(ctx, connect.NewRequest(&mimirv1.UpdateCloudGpuRequest{
		Enabled: true, TokenId: "ak-test", TokenSecret: "as-secret", Gpu: "A10G", ConsoleUrl: "https://mimir.example.com/", IdleMinutes: 3, MaxHours: 2, Capacity: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	g := up.Msg.GetCloudGpu()
	if !g.GetEnabled() || g.GetWorkerId() == "" || !g.GetHasTokenSecret() || g.GetConsoleUrl() != "https://mimir.example.com" || g.GetState() != gpucloud.StateAsleep {
		t.Fatalf("after setup: %v", g)
	}
	// Changing it keeps the stored secret and the same worker.
	up, err = own.workers.UpdateCloudGpu(ctx, connect.NewRequest(&mimirv1.UpdateCloudGpuRequest{
		Enabled: true, TokenId: "ak-test", Gpu: "L4", ConsoleUrl: "https://mimir.example.com", IdleMinutes: 3, MaxHours: 2, Capacity: 1,
	}))
	if err != nil || up.Msg.GetCloudGpu().GetWorkerId() != g.GetWorkerId() || !up.Msg.GetCloudGpu().GetHasTokenSecret() || gpu.Config().TokenSecret != "as-secret" {
		t.Fatalf("change = %v, %v", up, err)
	}
	// Another provider doesn't get Modal's secret.
	if _, err := own.workers.UpdateCloudGpu(ctx, connect.NewRequest(&mimirv1.UpdateCloudGpuRequest{
		Enabled: true, Provider: "runpod", Gpu: "NVIDIA L4", ConsoleUrl: "https://mimir.example.com",
	})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("moving to RunPod without a key: %v", err)
	}

	// Its worker teaches only, and its key is known before it joins.
	ls, _ := own.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{}))
	if len(ls.Msg.GetWorkers()) != 1 {
		t.Fatalf("workers = %v", ls.Msg.GetWorkers())
	}
	w := ls.Msg.GetWorkers()[0]
	if w.GetId() != g.GetWorkerId() || w.GetName() != api.CloudGPUWorkerName || w.GetTasks() != "train" || w.GetKeyFingerprint() == "" || w.GetOnline() {
		t.Fatalf("GPU's worker = %v", w)
	}
	if _, err := own.workers.ReplaceWorkerToken(ctx, connect.NewRequest(&mimirv1.ReplaceWorkerTokenRequest{Id: w.GetId()})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ReplaceWorkerToken on the GPU's worker: %v", err)
	}

	// Nothing waits, so nothing starts.
	gpu.Step(ctx)
	if len(fake.started) != 0 {
		t.Fatal("started a GPU with nothing to train")
	}

	// The console hands its program only to its own workers.
	cfg := gpu.Config()
	secret, _, _ := strings.Cut(cfg.Token, ".")
	for _, c := range []struct {
		auth string
		want int
	}{{"", 401}, {"Bearer mimirw_nope", 401}, {"Bearer " + secret, 200}} {
		req, _ := http.NewRequest("GET", srv.URL+gpucloud.ProgramPath, nil)
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Fatalf("program with %q = %d", c.auth, res.StatusCode)
		}
		if c.want == 200 {
			exe, _ := os.Executable()
			st, _ := os.Stat(exe)
			if n != st.Size() {
				t.Fatalf("program is %d bytes, want %d", n, st.Size())
			}
		}
	}

	// A run waits that nothing here takes: the GPU starts, joins and
	// trains it.
	svc.Local, runner.Local = false, false
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit sorter", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	gpu.Step(ctx)
	if len(fake.started) != 1 || get().GetState() != gpucloud.StateStarting {
		t.Fatalf("started %d, state %v", len(fake.started), get())
	}
	m := fake.started[0]
	if m.GPU != "L4" || m.Timeout != 2*time.Hour || m.Env["MIMIR_DEVICE"] != "gpu" || m.Secrets["MIMIR_WORKER_TOKEN"] != cfg.Token {
		t.Fatalf("machine = %+v", m)
	}
	script := m.Script()
	if !strings.Contains(script, "https://mimir.example.com"+gpucloud.ProgramPath) || !strings.Contains(script, "-tasks=train") || !strings.Contains(script, "-exit-when-idle 3m") || strings.Contains(script, secret) {
		t.Fatalf("command = %s", script)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !svc.Pool.Online(w.GetId()) {
		if time.Now().After(deadline) {
			t.Fatal("the GPU's worker never joined")
		}
		time.Sleep(20 * time.Millisecond)
	}
	gpu.Step(ctx)
	if get().GetState() != gpucloud.StateRunning {
		t.Fatalf("joined: %v", get())
	}
	gpu.Step(ctx) // still one machine
	if len(fake.started) != 1 {
		t.Fatalf("started %d machines", len(fake.started))
	}
	runner.Tick(ctx)
	j, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: job.Msg.GetJob().GetId()}))
	if jb := j.Msg.GetJob(); jb.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || jb.GetWorker() != api.CloudGPUWorkerName {
		t.Fatalf("job = %v", jb)
	}

	// With nothing more to do it stops itself.
	deadline = time.Now().Add(10 * time.Second)
	for get().GetState() != gpucloud.StateAsleep {
		if time.Now().After(deadline) {
			t.Fatalf("still %v", get())
		}
		time.Sleep(100 * time.Millisecond)
		gpu.Step(ctx)
	}

	// Removing its worker turns renting off.
	if _, err := own.workers.DeleteWorker(ctx, connect.NewRequest(&mimirv1.DeleteWorkerRequest{Id: w.GetId()})); err != nil {
		t.Fatal(err)
	}
	if g := get(); g.GetEnabled() || g.GetWorkerId() != "" || g.GetState() != gpucloud.StateOff {
		t.Fatalf("after removing its worker: %v", g)
	}
}
