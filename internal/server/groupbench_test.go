package server_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	builtin "github.com/gsoultan/mimir-server/internal/worker"
	"github.com/gsoultan/mimir-server/worker"
)

// TestGroupBench times one run on 1, 2 and 3 workers together, each a
// process of its own pinned to one CPU, and reports each worker's peak
// memory. It runs only when MIMIR_GROUP_BENCH is set (and needs taskset
// and at least four CPUs); for example
//
//	MIMIR_GROUP_BENCH=1 go test -run TestGroupBench -v ./internal/server
func TestGroupBench(t *testing.T) {
	if os.Getenv("MIMIR_GROUP_BENCH") == "" {
		t.Skip("set MIMIR_GROUP_BENCH to run")
	}
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	svc.Local, runner.Local = false, false
	runner.GroupWorkers = svc.GroupWorkers
	t.Cleanup(func() { runner.GroupWorkers = nil })

	type proc struct {
		cmd *exec.Cmd
		id  string
	}
	var procs []proc
	for i := range 3 {
		created, err := own.workers.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: fmt.Sprintf("small-%d", i+1)}))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("taskset", "-c", strconv.Itoa(i+1), os.Args[0], "-test.run=^TestBenchWorker$")
		cmd.Env = append(os.Environ(), "GOMAXPROCS=1", "MIMIR_BENCH_SERVER="+srv.URL, "MIMIR_BENCH_TOKEN="+created.Msg.GetToken(), "MIMIR_BENCH_DIR="+t.TempDir())
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		procs = append(procs, proc{cmd, created.Msg.GetWorker().GetId()})
	}
	for _, p := range procs {
		waitOnline(t, own, p.id, true)
	}
	// Only the first n workers take training runs.
	use := func(n int) {
		for i, p := range procs {
			tasks := "train"
			if i >= n {
				tasks = "serve"
			}
			if _, err := own.workers.SetWorkerTasks(ctx, connect.NewRequest(&mimirv1.SetWorkerTasksRequest{Id: p.id, Tasks: tasks})); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	peak := func(i int) int64 {
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", procs[i].cmd.Process.Pid))
		for line := range strings.SplitSeq(string(b), "\n") {
			if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
				kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
				return kb << 10
			}
		}
		return 0
	}

	type workload struct {
		name, arch string
		ds         *mimirv1.CreateDatasetRequest
		settings   *mimirv1.TrainingSettings
	}
	loads := []workload{
		{"table, 200,000 rows", "table-mlp", &mimirv1.CreateDatasetRequest{Name: "Big table", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: benchTable(200_000)}, &mimirv1.TrainingSettings{Epochs: 3}},
		{"photos, 2,000", "photo-cnn", &mimirv1.CreateDatasetRequest{Name: "Photos", Kind: mimirv1.DatasetKind_DATASET_KIND_PHOTOS, Archive: benchPhotos(2000)}, &mimirv1.TrainingSettings{Epochs: 3}},
	}
	if only := os.Getenv("MIMIR_GROUP_BENCH_ONLY"); only != "" {
		loads = slicesFilter(loads, func(w workload) bool { return strings.Contains(w.arch, only) })
	}
	for _, w := range loads {
		ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(w.ds))
		if err != nil {
			t.Fatal(err)
		}
		var base time.Duration
		for n := 1; n <= 3; n++ {
			use(n)
			if _, err := own.settings.UpdateTraining(ctx, connect.NewRequest(&mimirv1.UpdateTrainingRequest{GroupWorkers: new(int32(n))})); err != nil {
				t.Fatal(err)
			}
			j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: w.name, Architecture: w.arch, DatasetId: ds.Msg.GetDataset().GetId(), Settings: w.settings}))
			if err != nil {
				t.Fatal(err)
			}
			runner.Tick(ctx)
			got, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
			if err != nil {
				t.Fatal(err)
			}
			job := got.Msg.GetJob()
			if job.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
				t.Fatalf("%s on %d: %v", w.name, n, job)
			}
			took := job.GetEndTime().AsTime().Sub(job.GetStartTime().AsTime())
			if n == 1 {
				base = took
			}
			h := job.GetHistory()
			var mem []string
			for i := range n {
				mem = append(mem, fmt.Sprintf("%d MiB", peak(i)>>20))
			}
			var epochs []string
			for _, e := range h {
				epochs = append(epochs, (time.Duration(e.GetDurationMillis()) * time.Millisecond).String())
			}
			t.Logf("%s on %d worker(s) [%s]: %s (%.2fx), epochs %s, %d steps an epoch, accuracy %.3f, peak memory %s",
				w.name, n, job.GetWorker(), took.Round(10*time.Millisecond), base.Seconds()/took.Seconds(), strings.Join(epochs, " "), job.GetStepsPerEpoch(), h[len(h)-1].GetAccuracy(), strings.Join(mem, ", "))
		}
	}
}

// TestBenchWorker is a worker process of TestGroupBench.
func TestBenchWorker(t *testing.T) {
	url := os.Getenv("MIMIR_BENCH_SERVER")
	if url == "" {
		t.Skip("a worker process of TestGroupBench")
	}
	dir := os.Getenv("MIMIR_BENCH_DIR")
	worker.Run(context.Background(), worker.Config{Server: url, Token: os.Getenv("MIMIR_BENCH_TOKEN"), Capacity: 1, Version: "bench", DataDir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, builtin.Builtin(dir))
}

func slicesFilter[T any](s []T, keep func(T) bool) []T {
	var out []T
	for _, v := range s {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

// benchTable writes rows of 16 numbers, a category and a class that
// depends on them.
func benchTable(rows int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	var b bytes.Buffer
	for j := range 16 {
		fmt.Fprintf(&b, "f%d,", j)
	}
	b.WriteString("color,label\n")
	colors := []string{"red", "green", "blue", "gray"}
	for range rows {
		s := 0.0
		for j := range 16 {
			v := r.NormFloat64()
			if j < 4 {
				s += v
			}
			fmt.Fprintf(&b, "%.3f,", v)
		}
		c := r.IntN(len(colors))
		s += float64(c) - 1.5
		label := "low"
		switch {
		case s > 1:
			label = "high"
		case s > -1:
			label = "mid"
		}
		fmt.Fprintf(&b, "%s,%s\n", colors[c], label)
	}
	return b.Bytes()
}

// benchPhotos writes a ZIP of n noisy photos in two folders: a bright
// square in the top half, or in the bottom half.
func benchPhotos(n int) []byte {
	r := rand.New(rand.NewPCG(3, 4))
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for i := range n {
		img := image.NewRGBA(image.Rect(0, 0, 48, 48))
		for k := range img.Pix {
			img.Pix[k] = uint8(r.IntN(120))
		}
		top, folder := i%2 == 0, "bottom"
		if top {
			folder = "top"
		}
		x, y := r.IntN(28), r.IntN(10)
		if !top {
			y += 24
		}
		for dy := range 14 {
			for dx := range 14 {
				img.Set(x+dx, y+dy, color.RGBA{230, 230, 60, 255})
			}
		}
		f, _ := z.Create(fmt.Sprintf("%s/%04d.png", folder, i))
		png.Encode(f, img)
	}
	z.Close()
	return b.Bytes()
}
