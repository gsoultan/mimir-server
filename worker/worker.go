// Package worker is the SDK for a mimir-server worker: a process that
// connects to a server with a worker token and runs models for it.
//
// The server's own binary is a worker when started with -role=worker. A
// custom worker, for models a weights file can't describe, is a few lines:
//
//	handlers := map[string]worker.Handler{"my-arch": myHandler}
//	err := worker.Run(ctx, worker.Config{Server: "https://mimir.example.com", Token: token, Name: "gpu-1"}, handlers)
//
// Run keeps the link open, reconnecting with backoff, until ctx ends. A
// Handler that also implements Trainer is offered training tasks.
package worker

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// Model is a stored model the server asks a worker to run. Weights are the
// Mimir Model file the server holds for it.
type Model struct {
	ID           string
	Architecture string
	InputShape   []int
	Labels       []string
	Features     []string
	Weights      []byte
	// Version is the SHA-256 of Weights, hex-encoded. It changes when the
	// model is retrained in place under the same ID.
	Version string
}

// Prediction is a model's answer: one raw output and one probability per
// label.
type Prediction struct {
	Output        []float32
	Probabilities []float32
}

// Handler runs predictions for one architecture.
type Handler interface {
	Predict(ctx context.Context, m *Model, input []float32) (Prediction, error)
}

// Loader is a Handler that keeps its own loaded copy of each model, so the
// worker need not keep the raw weights too. The worker calls Load once
// with a model's weights and then drops them: Predict gets the Model with
// Weights empty. A Predict that needs the weights again, because the
// handler let its copy go, returns ErrNeedWeights and the worker asks the
// server to send them. Unload is called when the worker drops a model to
// stay within Config.CacheBytes.
type Loader interface {
	Load(ctx context.Context, m *Model) error
	Unload(modelID string)
}

// ErrNeedWeights is what a Loader's Predict returns when it no longer has
// the model loaded and the Model it was given carries no weights.
var ErrNeedWeights = errors.New("worker: the model's weights are needed again")

// Settings are a training task's knobs.
type Settings struct {
	Epochs       int
	BatchSize    int
	LearningRate float64
	HoldOut      float64
	// Layers, when set, is how someone shaped a table model: its hidden
	// steps in place of the usual ones.
	Layers []Layer
}

// Layer is one step of a designed table model: "dense" learns patterns
// with Units numbers, "dropout" forgets a Rate of them while learning.
type Layer struct {
	Kind  string
	Units int
	Rate  float64
}

// Epoch is how one pass over the examples went.
type Epoch struct {
	Epoch    int
	Loss     float64
	Accuracy float64
	ValLoss  float64
	Millis   int64
	// MAE is how far a number model's answers are off on average.
	MAE float64
}

// Dataset is the examples a training task learns from: a CSV table with a
// header row and a target column, a ZIP of photos, or the built-in
// handwritten digits.
type Dataset struct {
	Kind   string // "table", "photos", "photo_table" or "mnist"
	CSV    []byte
	Target string
	// Archive is the ZIP for "photos" (a folder per answer) and
	// "photo_table" (a data.csv and the photos it names).
	Archive []byte
	// Columns are the table's columns as first read; keep their kinds.
	Columns []Column
}

// Column is a table column and how it becomes numbers.
type Column struct {
	Name string
	// Kind is "number", "category", "yesno", "text" or "photo".
	Kind   string
	Values []string // a category's values, in encoding order
	Fill   float64  // what an empty number cell reads as
}

// TrainTask asks for a model to be trained.
type TrainTask struct {
	JobID        string
	Architecture string
	Settings     Settings
	Dataset      Dataset
	// History is what earlier attempts of this job finished.
	History []Epoch
	// BaseWeights, when set, are a model to continue learning from, while
	// the examples still have BaseLabels and BaseFeatures.
	BaseWeights  []byte
	BaseLabels   []string
	BaseFeatures []string
}

// Progress is where a training task is.
type Progress struct {
	Epoch   int // finished epochs
	Step    int // step within the next one
	Steps   int // steps in an epoch
	History []Epoch
}

// Trained is a finished training task.
type Trained struct {
	Weights    []byte
	History    []Epoch
	InputShape []int
	Labels     []string
	Features   []string
	Columns    []Column
	// Fresh says BaseWeights didn't fit and it learned from scratch.
	Fresh bool
}

// Trainer is a Handler that can also train its architecture.
type Trainer interface {
	Train(ctx context.Context, t TrainTask, progress func(Progress)) (Trained, error)
}

// Config says which server to join and as whom.
type Config struct {
	// Server is the server's address, such as https://mimir.example.com.
	Server string
	// Token is the worker token made under Settings → Workers.
	Token string
	// Name is only used in logs; the server shows the name the token was
	// made with.
	Name string
	// Device describes the hardware, such as "cpu" or "metal". Defaults to
	// the OS and architecture.
	Device string
	// Capacity is how many tasks to run at once. Defaults to the CPU count.
	Capacity int
	// Tasks is the kind of work the worker takes: TasksAll (the default),
	// TasksServe for predictions only, or TasksTrain for training runs
	// only. A machine dedicated to training keeps its load away from the
	// ones answering the API, and the other way round.
	Tasks Tasks
	// Version is reported to the server for display.
	Version string
	// Log receives connection events; nil means the default logger.
	Log *slog.Logger
	// HTTPClient is used for the link; nil picks one that speaks HTTP/2
	// to the server and checks the console's key when the token carries
	// its fingerprint.
	HTTPClient *http.Client
	// KeyFile is where the worker keeps the private key it proves itself
	// with. It is made on first start; keep it with the worker. Empty
	// means DefaultKeyFile in DataDir, or in the user's config folder
	// without one.
	KeyFile string
	// DataDir is the folder whose disk the dashboard reports, and where
	// the key is kept by default.
	DataDir string
	// Insecure allows an http:// server on another machine. Tokens and
	// examples then cross the network unencrypted; only use it on a
	// network you trust.
	Insecure bool
	// CacheBytes bounds the models the worker keeps, by the size of their
	// weights; the least recently used are dropped first and sent again
	// by the server when next needed. 0 means DefaultCacheBytes; a
	// negative value means no bound.
	CacheBytes int64
	// Key, when set, is the private key the worker proves itself with,
	// in place of the one in KeyFile. A worker on a machine that starts
	// fresh each time, such as a rented GPU, is given its key this way so
	// it is the same worker every time.
	Key ed25519.PrivateKey
	// ExitWhenIdle, when above zero, makes Run return nil once the worker
	// has run nothing for that long, so a machine rented by the minute
	// stops when the work is done. Zero keeps it running.
	ExitWhenIdle time.Duration
}

// DefaultCacheBytes is how many bytes of models a worker keeps when
// Config.CacheBytes isn't set.
const DefaultCacheBytes = 4 << 30

// Tasks is the kind of work a worker takes; see Config.Tasks.
type Tasks string

const (
	// TasksAll takes predictions and training runs alike.
	TasksAll Tasks = "all"
	// TasksServe answers predictions only. The console sends no training
	// runs to the worker.
	TasksServe Tasks = "serve"
	// TasksTrain takes training runs only. The console sends no
	// predictions to the worker, and a model it trains runs elsewhere.
	TasksTrain Tasks = "train"
)

// ParseTasks reads a Tasks value as typed on a command line; empty means
// TasksAll.
func ParseTasks(s string) (Tasks, error) {
	switch t := Tasks(strings.ToLower(strings.TrimSpace(s))); t {
	case "":
		return TasksAll, nil
	case TasksAll, TasksServe, TasksTrain:
		return t, nil
	default:
		return "", fmt.Errorf("worker: unknown tasks %q: use all, serve or train", s)
	}
}

// Run joins the server and serves tasks until ctx ends. It returns ctx's
// error, or a configuration error found before connecting. With
// Config.ExitWhenIdle it also returns nil once the worker has been idle
// that long.
func Run(ctx context.Context, cfg Config, handlers map[string]Handler) error {
	if cfg.Server == "" || cfg.Token == "" {
		return errors.New("worker: Server and Token are required")
	}
	if len(handlers) == 0 {
		return errors.New("worker: no handlers")
	}
	u, err := url.Parse(cfg.Server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("worker: Server must be an http(s) address, got %q", cfg.Server)
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	secret, pin := splitToken(cfg.Token)
	plain := u.Scheme == "http"
	switch {
	case plain && pin != "":
		return errors.New("worker: this token is for an https console; use https:// in the server address")
	case plain && !isLoopback(u.Hostname()) && !cfg.Insecure:
		return fmt.Errorf("worker: %s isn't encrypted; use https://, or allow it explicitly (-insecure) on a network you trust", cfg.Server)
	case plain && !isLoopback(u.Hostname()):
		cfg.Log.Warn("worker: connecting without encryption", "server", cfg.Server)
	}
	if cfg.KeyFile == "" && cfg.Key == nil {
		dir := cfg.DataDir
		if dir == "" {
			conf, err := os.UserConfigDir()
			if err != nil {
				return fmt.Errorf("worker: no folder for the key; set KeyFile: %w", err)
			}
			dir = filepath.Join(conf, "mimir-worker")
		}
		cfg.KeyFile = DefaultKeyFile(dir, cfg.Token)
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Dir(cfg.KeyFile)
	}
	key := cfg.Key
	if key == nil {
		if key, err = loadKey(cfg.KeyFile); err != nil {
			return err
		}
	} else if len(key) != ed25519.PrivateKeySize {
		return errors.New("worker: Key isn't an Ed25519 private key")
	}
	if cfg.Device == "" {
		cfg.Device = runtime.GOOS + "/" + runtime.GOARCH
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = runtime.NumCPU()
	}
	if cfg.Tasks, err = ParseTasks(string(cfg.Tasks)); err != nil {
		return err
	}
	if cfg.Tasks == TasksTrain {
		for name, h := range handlers {
			if _, ok := h.(Trainer); !ok {
				return fmt.Errorf("worker: Tasks is train, but the handler for %s doesn't train", name)
			}
		}
	}
	client := cfg.HTTPClient
	if client == nil {
		var tc *tls.Config
		if pin != "" {
			if tc, err = pinned(pin); err != nil {
				return err
			}
		}
		client = httpClient(plain, tc)
	}
	cfg.Token = secret
	switch {
	case cfg.CacheBytes == 0:
		cfg.CacheBytes = DefaultCacheBytes
	case cfg.CacheBytes < 0:
		cfg.CacheBytes = 0 // no bound
	}
	w := &worker{cfg: cfg, handlers: handlers, key: key, stats: newSampler(cfg.DataDir), lastActive: time.Now(), client: mimirv1connect.NewWorkerServiceClient(client, strings.TrimRight(cfg.Server, "/"), connect.WithGRPC(), connect.WithSendMaxBytes(maxBytes), connect.WithReadMaxBytes(maxBytes))}

	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if cfg.ExitWhenIdle > 0 {
		go w.exitWhenIdle(ctx, stop)
	}
	delay := time.Second
	for {
		started := time.Now()
		err := w.serve(ctx)
		if errors.Is(context.Cause(ctx), errIdle) {
			cfg.Log.Info("worker: idle, stopping", "after", cfg.ExitWhenIdle)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > time.Minute {
			delay = time.Second // it was up for a while; try again soon
		}
		if c := connect.CodeOf(err); c == connect.CodeUnauthenticated || c == connect.CodePermissionDenied {
			cfg.Log.Error("worker: the console refused this worker; check its token under Settings → Workers", "error", err)
			delay = 30 * time.Second
		} else {
			cfg.Log.Warn("worker: link ended, reconnecting", "error", err, "in", delay)
		}
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), errIdle) {
				return nil
			}
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 30*time.Second)
	}
}

// errIdle is the cause Run's context ends with when the worker stops for
// being idle.
var errIdle = errors.New("worker: idle")

// exitWhenIdle ends Run, through stop, once nothing has run for
// cfg.ExitWhenIdle.
func (w *worker) exitWhenIdle(ctx context.Context, stop context.CancelCauseFunc) {
	check := time.NewTicker(min(w.cfg.ExitWhenIdle/4, 15*time.Second))
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
		}
		w.mu.Lock()
		idle := w.active == 0 && time.Since(w.lastActive) >= w.cfg.ExitWhenIdle
		w.mu.Unlock()
		if idle {
			stop(errIdle)
			return
		}
	}
}

// maxBytes bounds one message on the link: a weights file or a table.
const maxBytes = 1 << 30

type worker struct {
	cfg      Config
	handlers map[string]Handler
	client   mimirv1connect.WorkerServiceClient
	key      ed25519.PrivateKey
	stats    *sampler

	sendMu sync.Mutex
	stream *connect.BidiStreamForClient[mimirv1.LinkRequest, mimirv1.LinkResponse]

	models *modelCache

	mu      sync.Mutex
	running map[string]context.CancelFunc
	// active is how many tasks are running, and lastActive when one last
	// started or finished, for Config.ExitWhenIdle. They outlive a link.
	active     int
	lastActive time.Time
	// tasks is what this link takes: the console's choice from the
	// welcome, else cfg.Tasks. Set before any task arrives.
	tasks Tasks
}

func (w *worker) serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := w.client.Link(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+w.cfg.Token)
	w.stream = stream
	go func() { // a blocked Receive only wakes when the stream closes
		<-ctx.Done()
		stream.CloseRequest()
		stream.CloseResponse()
	}()
	w.models = newModelCache(w.cfg.CacheBytes)
	w.running = map[string]context.CancelFunc{}

	archs := make([]string, 0, len(w.handlers))
	trains := w.cfg.Tasks != TasksServe
	for name, h := range w.handlers {
		archs = append(archs, name)
		if _, ok := h.(Trainer); !ok {
			trains = false
		}
	}
	slices.Sort(archs)
	hello := &mimirv1.Hello{Device: w.cfg.Device, Cpus: int32(runtime.NumCPU()), MemoryBytes: memory(), Architectures: archs, Trains: trains, TrainingOnly: w.cfg.Tasks == TasksTrain, Capacity: int32(w.cfg.Capacity), Version: w.cfg.Version,
		PublicKey: w.key.Public().(ed25519.PublicKey), Stats: w.stats.read()}
	if err := w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Hello{Hello: hello}}); err != nil {
		return err
	}
	first, err := stream.Receive()
	if err != nil {
		return err
	}
	if c := first.GetChallenge(); c != nil {
		// Prove this is the machine the token joined from.
		if err := w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Proof{Proof: &mimirv1.Proof{Signature: sign(w.key, c.Nonce)}}}); err != nil {
			return err
		}
		if first, err = stream.Receive(); err != nil {
			return err
		}
	}
	welcome := first.GetWelcome()
	if welcome == nil {
		return errors.New("worker: the server didn't say welcome")
	}
	w.tasks = w.cfg.Tasks
	if t, err := ParseTasks(welcome.Tasks); err == nil && welcome.Tasks != "" {
		w.tasks = t // the console's choice, from the worker's page
	}
	w.cfg.Log.Info("worker: linked", "server", w.cfg.Server, "as", welcome.Name, "tasks", string(w.tasks))

	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-beat.C:
				if w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Heartbeat{Heartbeat: &mimirv1.Heartbeat{Stats: w.stats.read()}}}) != nil {
					return
				}
			}
		}
	}()

	var tasks sync.WaitGroup
	defer tasks.Wait()
	slots := make(chan struct{}, w.cfg.Capacity)
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return errors.New("worker: the server closed the link")
		}
		if err != nil {
			return err
		}
		switch b := msg.Body.(type) {
		case *mimirv1.LinkResponse_Predict:
			tasks.Go(func() {
				w.slot(ctx, slots, b.Predict.TaskId, func(tctx context.Context) { w.predict(tctx, b.Predict) })
			})
		case *mimirv1.LinkResponse_Train:
			tasks.Go(func() { w.slot(ctx, slots, b.Train.TaskId, func(tctx context.Context) { w.train(tctx, b.Train) }) })
		case *mimirv1.LinkResponse_Cancel:
			w.mu.Lock()
			if cancel := w.running[b.Cancel.TaskId]; cancel != nil {
				cancel()
			}
			w.mu.Unlock()
		}
	}
}

// slot runs f when a slot is free, cancellable by task id.
func (w *worker) slot(ctx context.Context, slots chan struct{}, id string, f func(context.Context)) {
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-slots }()
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.mu.Lock()
	w.running[id] = cancel
	w.active++
	w.lastActive = time.Now()
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.running, id)
		w.active--
		w.lastActive = time.Now()
		w.mu.Unlock()
	}()
	f(tctx)
}

func (w *worker) send(msg *mimirv1.LinkRequest) error {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	return w.stream.Send(msg)
}

func (w *worker) predict(ctx context.Context, t *mimirv1.PredictTask) {
	reply := func(r *mimirv1.PredictResult) {
		r.TaskId = t.TaskId
		w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_PredictResult{PredictResult: r}})
	}
	if w.tasks == TasksTrain {
		reply(&mimirv1.PredictResult{Error: "this worker only trains models; it doesn't answer predictions"})
		return
	}
	h := w.handlers[t.Architecture]
	if h == nil {
		reply(&mimirv1.PredictResult{Error: "this worker doesn't run " + t.Architecture})
		return
	}
	var m *Model
	if len(t.Weights) > 0 {
		m = &Model{ID: t.ModelId, Architecture: t.Architecture, InputShape: ints(t.GetSpec().GetInputShape()), Labels: t.GetSpec().GetLabels(), Features: t.GetSpec().GetFeatures(), Weights: t.Weights, Version: version(t.Weights)}
		size := int64(len(t.Weights))
		var unload func()
		if ld, ok := h.(Loader); ok {
			if err := ld.Load(ctx, m); err != nil {
				reply(&mimirv1.PredictResult{Error: err.Error()})
				return
			}
			m.Weights = nil // the handler has its own copy now
			unload = func() { ld.Unload(m.ID) }
		}
		w.models.put(m, size, unload)
	} else {
		m = w.models.get(t.ModelId)
	}
	if m == nil {
		reply(&mimirv1.PredictResult{NeedWeights: true})
		return
	}
	p, err := h.Predict(ctx, m, t.GetInput().GetValues())
	if errors.Is(err, ErrNeedWeights) {
		w.models.remove(m)
		reply(&mimirv1.PredictResult{NeedWeights: true})
		return
	}
	if err != nil {
		reply(&mimirv1.PredictResult{Error: err.Error()})
		return
	}
	reply(&mimirv1.PredictResult{
		Output:        &mimirv1.Tensor{Shape: []int64{int64(len(p.Output))}, Values: p.Output},
		Probabilities: &mimirv1.Tensor{Shape: []int64{int64(len(p.Probabilities))}, Values: p.Probabilities},
	})
}

func (w *worker) train(ctx context.Context, t *mimirv1.TrainTask) {
	reply := func(r *mimirv1.TrainResult) {
		r.TaskId = t.TaskId
		w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_TrainResult{TrainResult: r}})
	}
	tr, _ := w.handlers[t.Architecture].(Trainer)
	if w.tasks == TasksServe {
		tr = nil // it only answers; the console knows and sends none
	}
	if tr == nil {
		reply(&mimirv1.TrainResult{Error: "this worker doesn't train " + t.Architecture})
		return
	}
	kind := map[mimirv1.DatasetKind]string{
		mimirv1.DatasetKind_DATASET_KIND_MNIST:       "mnist",
		mimirv1.DatasetKind_DATASET_KIND_PHOTOS:      "photos",
		mimirv1.DatasetKind_DATASET_KIND_PHOTO_TABLE: "photo_table",
	}[t.DatasetKind]
	if kind == "" {
		kind = "table"
	}
	task := TrainTask{
		JobID: t.JobId, Architecture: t.Architecture,
		Settings:    Settings{Epochs: int(t.GetSettings().GetEpochs()), BatchSize: int(t.GetSettings().GetBatchSize()), LearningRate: t.GetSettings().GetLearningRate(), HoldOut: t.GetSettings().GetHoldOut(), Layers: layersFromProto(t.GetSettings().GetLayers())},
		Dataset:     Dataset{Kind: kind, CSV: t.Csv, Target: t.Target, Archive: t.Archive, Columns: columnsFromProto(t.Columns)},
		History:     epochsFromProto(t.History),
		BaseWeights: t.BaseWeights, BaseLabels: t.BaseLabels, BaseFeatures: t.BaseFeatures,
	}
	var last time.Time
	res, err := tr.Train(ctx, task, func(p Progress) {
		if p.Step != 0 && time.Since(last) < time.Second {
			return // a step is worth sending once a second; an epoch always
		}
		last = time.Now()
		w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_TrainProgress{TrainProgress: &mimirv1.TrainProgress{
			TaskId: t.TaskId, Epoch: int32(p.Epoch), Step: int32(p.Step), StepsPerEpoch: int32(p.Steps), History: epochsToProto(p.History),
		}}})
	})
	switch {
	case ctx.Err() != nil:
		reply(&mimirv1.TrainResult{Cancelled: true})
	case err != nil:
		reply(&mimirv1.TrainResult{Error: err.Error()})
	default:
		reply(&mimirv1.TrainResult{
			Weights: res.Weights, History: epochsToProto(res.History), Fresh: res.Fresh,
			Spec: &mimirv1.ModelSpec{InputShape: int64s(res.InputShape), Labels: res.Labels, Features: res.Features, Columns: columnsToProto(res.Columns)},
		})
	}
}

// memory returns the machine's memory in bytes where it is easy to know.
func memory() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			var kb int64
			fmt.Sscan(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), &kb)
			return kb * 1024
		}
	}
	return 0
}

func epochsToProto(h []Epoch) []*mimirv1.EpochResult {
	out := make([]*mimirv1.EpochResult, len(h))
	for i, e := range h {
		out[i] = &mimirv1.EpochResult{Epoch: int32(e.Epoch), Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, DurationMillis: e.Millis, TypicalError: e.MAE}
	}
	return out
}

func epochsFromProto(h []*mimirv1.EpochResult) []Epoch {
	out := make([]Epoch, len(h))
	for i, e := range h {
		out[i] = Epoch{Epoch: int(e.Epoch), Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, Millis: e.DurationMillis, MAE: e.TypicalError}
	}
	return out
}

func columnsToProto(cols []Column) []*mimirv1.Column {
	out := make([]*mimirv1.Column, len(cols))
	for i, c := range cols {
		out[i] = &mimirv1.Column{Name: c.Name, Kind: c.Kind, Values: c.Values, Fill: c.Fill}
	}
	return out
}

func columnsFromProto(cols []*mimirv1.Column) []Column {
	out := make([]Column, len(cols))
	for i, c := range cols {
		out[i] = Column{Name: c.GetName(), Kind: c.GetKind(), Values: c.GetValues(), Fill: c.GetFill()}
	}
	return out
}

func int64s(v []int) []int64 {
	out := make([]int64, len(v))
	for i, x := range v {
		out[i] = int64(x)
	}
	return out
}

// version is the hex SHA-256 of weights, the Model's Version.
func version(weights []byte) string {
	sum := sha256.Sum256(weights)
	return hex.EncodeToString(sum[:])
}

func ints(v []int64) []int {
	out := make([]int, len(v))
	for i, x := range v {
		out[i] = int(x)
	}
	return out
}

func layersFromProto(ls []*mimirv1.Layer) []Layer {
	out := make([]Layer, len(ls))
	for i, l := range ls {
		out[i] = Layer{Kind: l.GetKind(), Units: int(l.GetUnits()), Rate: l.GetRate()}
	}
	return out
}
