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
	"cmp"
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
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/memguard"
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
// header row and a target column, a ZIP of photos, the built-in
// handwritten digits, or writing a language model is fine-tuned on.
type Dataset struct {
	Kind   string // "table", "photos", "photo_table", "mnist" or "text"
	CSV    []byte
	Target string
	// Archive is the ZIP for "photos" (a folder per answer) and
	// "photo_table" (a data.csv and the photos it names).
	Archive []byte
	// Text is the file of "text": plain text, or JSON Lines of chats,
	// prompts and completions, or passages.
	Text []byte
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
	// BaseAdapter, for a fine-tune of a language model, is the LoRA
	// adapter of the fine-tune it continues; BaseWeights are then the
	// language model's file.
	BaseAdapter []byte
	// Group, when set, is the run's other workers: this one trains its
	// share of the batches and averages weights with them (see
	// GroupTrainer). Only member 0's progress and weights are kept.
	Group *Group
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
	// MemoryLimit is the most memory the worker lets itself use, in
	// bytes. As it nears it the worker lets go of models, turns down
	// training runs, and stops the ones it has (they go back to the
	// console's queue and resume from their checkpoints), so it never
	// runs out. 0 finds it from $GOMEMLIMIT, the container's limit or
	// the machine's memory; negative turns it off.
	MemoryLimit int64
	// Memory, when set, is the guard to keep within in place of one made
	// from MemoryLimit; Run measures with it. The server's binary passes
	// its own, so the models it runs shrink their work with it too.
	Memory *memguard.Guard
	// StopTrainingAfter is how long memory may stay critical before the
	// runs training here are stopped and handed back to the console's
	// queue; until then they keep going. Zero means a minute.
	StopTrainingAfter time.Duration
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
	w.memory = cfg.Memory
	if w.memory == nil {
		w.memory = memguard.New(memguard.Config{Limit: cfg.MemoryLimit, Log: cfg.Log, Name: "worker"})
	}
	w.stats.memory = w.memory
	w.memory.Watch(w.memoryChanged)
	w.memory.StaysCritical(cmp.Or(cfg.StopTrainingAfter, time.Minute), w.stopTraining)
	go w.memory.Run(ctx)

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

	memory *memguard.Guard

	mu      sync.Mutex
	models  *modelCache // the link's; guarded by mu
	running map[string]task
	// active is how many tasks are running, and lastActive when one last
	// started or finished, for Config.ExitWhenIdle. They outlive a link.
	active     int
	lastActive time.Time
	// tasks is what this link takes: the console's choice from the
	// welcome, else cfg.Tasks. Set before any task arrives.
	tasks Tasks
	// groups are the training tasks trained together with other
	// workers, by task id, for the console's means.
	groups map[string]*Group
}

func (w *worker) serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := w.client.Link(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+w.cfg.Token)
	w.sendMu.Lock()
	w.stream = stream
	w.sendMu.Unlock()
	go func() { // a blocked Receive only wakes when the stream closes
		<-ctx.Done()
		stream.CloseRequest()
		stream.CloseResponse()
	}()
	w.mu.Lock()
	w.models = newModelCache(w.cfg.CacheBytes)
	w.running = map[string]task{}
	w.mu.Unlock()

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
	if trains {
		hello.GroupArchitectures = groupArchitectures(w.handlers)
	}
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
				w.slot(ctx, slots, b.Predict.TaskId, false, func(tctx context.Context) { w.predict(tctx, b.Predict) })
			})
		case *mimirv1.LinkResponse_Train:
			tasks.Go(func() {
				w.slot(ctx, slots, b.Train.TaskId, true, func(tctx context.Context) { w.train(tctx, b.Train) })
			})
		case *mimirv1.LinkResponse_Load:
			// Loading takes no slot: predictions keep flowing, answered
			// by the version already here until this one is in.
			tasks.Go(func() { w.load(ctx, b.Load) })
		case *mimirv1.LinkResponse_GroupMean:
			w.deliverMean(b.GroupMean)
		case *mimirv1.LinkResponse_Forget:
			w.mu.Lock()
			models := w.models
			w.mu.Unlock()
			models.forget(b.Forget.GetModelId())
		case *mimirv1.LinkResponse_Cancel:
			w.mu.Lock()
			if t, ok := w.running[b.Cancel.TaskId]; ok {
				t.cancel(context.Canceled)
			}
			w.mu.Unlock()
		}
	}
}

// task is a task the worker is running.
type task struct {
	cancel context.CancelCauseFunc
	train  bool
}

// slot runs f when a slot is free, cancellable by task id.
func (w *worker) slot(ctx context.Context, slots chan struct{}, id string, train bool, f func(context.Context)) {
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-slots }()
	tctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	w.mu.Lock()
	w.running[id] = task{cancel: cancel, train: train}
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

// memoryChanged acts on a new memory level: from high on the least
// recently used models go (half of them, then all but the last used when
// critical) and no new runs start, while the runs here keep going. The
// console hears the new level at once, so it sends work elsewhere while
// this worker recovers.
func (w *worker) memoryChanged(l memguard.Level) {
	w.mu.Lock()
	models := w.models
	w.mu.Unlock()
	if models != nil && l >= memguard.High {
		keep := int64(0)
		if l == memguard.High {
			used, _ := models.usage()
			keep = used / 2
		}
		if freed := models.shed(keep); freed > 0 {
			w.cfg.Log.Warn("worker: memory is short; let go of models", "bytes", freed, "level", l.String())
		}
	}
	go func() { // tell the console now rather than at the next heartbeat
		if w.linked() {
			w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Heartbeat{Heartbeat: &mimirv1.Heartbeat{Stats: w.stats.read()}}})
		}
	}()
}

// stopTraining stops the runs training here, for memory that stayed
// critical: they go back to the console's queue and resume from their
// checkpoints, here or elsewhere.
func (w *worker) stopTraining() {
	w.mu.Lock()
	var stop []context.CancelCauseFunc
	for _, t := range w.running {
		if t.train {
			stop = append(stop, t.cancel)
		}
	}
	w.mu.Unlock()
	for _, cancel := range stop {
		cancel(memguard.ErrLowMemory)
	}
	if len(stop) > 0 {
		w.cfg.Log.Warn("worker: memory stayed critical; stopped training runs, which go back to the console's queue", "runs", len(stop))
	}
}

// linked reports whether a link is open to send on.
func (w *worker) linked() bool {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	return w.stream != nil
}

// lowMemoryReply is what a task turned down for memory says.
const lowMemoryReply = "the worker is low on memory right now"

// recovered, deferred in a task, answers it with an error when it
// panicked, so one bad task fails alone and the worker keeps serving.
func (w *worker) recovered(what string, reply func(msg string)) {
	p := recover()
	if p == nil {
		return
	}
	w.cfg.Log.Error("worker: panic recovered; the worker carries on", "in", what, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
	reply(fmt.Sprintf("the worker hit an internal error in %s: %v", what, p))
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
	defer w.recovered("a prediction with "+t.Architecture, func(msg string) { reply(&mimirv1.PredictResult{Error: msg}) })
	w.mu.Lock()
	models := w.models
	w.mu.Unlock()
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
	if len(t.Weights) > 0 && w.memory.Level() == memguard.Critical {
		// Loading takes memory on top of the weights; the models
		// already loaded keep answering.
		reply(&mimirv1.PredictResult{Error: lowMemoryReply + " to load another model; models it has loaded keep answering"})
		return
	}
	if len(t.Weights) > 0 {
		var err error
		if m, err = w.install(ctx, h, t.ModelId, t.Architecture, t.GetSpec(), t.Weights); err != nil {
			reply(&mimirv1.PredictResult{Error: err.Error()})
			return
		}
	} else {
		m = models.get(t.ModelId)
	}
	if m == nil {
		reply(&mimirv1.PredictResult{NeedWeights: true})
		return
	}
	p, err := h.Predict(ctx, m, t.GetInput().GetValues())
	if errors.Is(err, ErrNeedWeights) {
		models.remove(m)
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

// install hands a model's weights to its handler and keeps the model,
// replacing the version held before. It returns what Predict is given.
func (w *worker) install(ctx context.Context, h Handler, modelID, archName string, spec *mimirv1.ModelSpec, weights []byte) (*Model, error) {
	m := &Model{ID: modelID, Architecture: archName, InputShape: ints(spec.GetInputShape()), Labels: spec.GetLabels(), Features: spec.GetFeatures(), Weights: weights, Version: version(weights)}
	size := int64(len(weights))
	var unload func()
	if ld, ok := h.(Loader); ok {
		if err := ld.Load(ctx, m); err != nil {
			return nil, err
		}
		m.Weights = nil // the handler has its own copy now
		unload = func() { ld.Unload(m.ID) }
	}
	w.mu.Lock()
	models := w.models
	w.mu.Unlock()
	models.put(m, size, unload)
	return m, nil
}

// load takes a model's weights ahead of its first prediction.
func (w *worker) load(ctx context.Context, t *mimirv1.LoadTask) {
	reply := func(errText string) {
		w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_LoadResult{LoadResult: &mimirv1.LoadResult{TaskId: t.TaskId, Error: errText}}})
	}
	defer w.recovered("loading a "+t.Architecture+" model", reply)
	if w.tasks == TasksTrain {
		reply("this worker only trains models; it doesn't answer predictions")
		return
	}
	if w.memory.Level() == memguard.Critical {
		reply(lowMemoryReply + " to load another model; models it has loaded keep answering")
		return
	}
	h := w.handlers[t.Architecture]
	if h == nil {
		reply("this worker doesn't run " + t.Architecture)
		return
	}
	if _, err := w.install(ctx, h, t.ModelId, t.Architecture, t.GetSpec(), t.Weights); err != nil {
		reply(err.Error())
		return
	}
	reply("")
}

func (w *worker) train(ctx context.Context, t *mimirv1.TrainTask) {
	reply := func(r *mimirv1.TrainResult) {
		r.TaskId = t.TaskId
		w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_TrainResult{TrainResult: r}})
	}
	defer w.recovered("training "+t.Architecture, func(msg string) { reply(&mimirv1.TrainResult{Error: msg}) })
	tr, _ := w.handlers[t.Architecture].(Trainer)
	if w.tasks == TasksServe {
		tr = nil // it only answers; the console knows and sends none
	}
	if tr == nil {
		reply(&mimirv1.TrainResult{Error: "this worker doesn't train " + t.Architecture})
		return
	}
	if w.memory.Level() != memguard.OK {
		// Back to the queue, for when this worker or another has room.
		reply(&mimirv1.TrainResult{LowMemory: true, Error: lowMemoryReply})
		return
	}
	kind := map[mimirv1.DatasetKind]string{
		mimirv1.DatasetKind_DATASET_KIND_MNIST:       "mnist",
		mimirv1.DatasetKind_DATASET_KIND_PHOTOS:      "photos",
		mimirv1.DatasetKind_DATASET_KIND_PHOTO_TABLE: "photo_table",
		mimirv1.DatasetKind_DATASET_KIND_TEXT:        "text",
	}[t.DatasetKind]
	if kind == "" {
		kind = "table"
	}
	task := TrainTask{
		JobID: t.JobId, Architecture: t.Architecture,
		Settings:    Settings{Epochs: int(t.GetSettings().GetEpochs()), BatchSize: int(t.GetSettings().GetBatchSize()), LearningRate: t.GetSettings().GetLearningRate(), HoldOut: t.GetSettings().GetHoldOut(), Layers: layersFromProto(t.GetSettings().GetLayers())},
		Dataset:     Dataset{Kind: kind, CSV: t.Csv, Target: t.Target, Archive: t.Archive, Text: t.Text, Columns: columnsFromProto(t.Columns)},
		History:     epochsFromProto(t.History),
		BaseWeights: t.BaseWeights, BaseLabels: t.BaseLabels, BaseFeatures: t.BaseFeatures, BaseAdapter: t.BaseAdapter,
	}
	if t.GroupSize > 1 {
		if _, ok := tr.(GroupTrainer); !ok {
			reply(&mimirv1.TrainResult{Error: "this worker doesn't train " + t.Architecture + " together with others"})
			return
		}
		g, done := w.groupFor(t)
		defer done()
		task.Group = g
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
	case errors.Is(context.Cause(ctx), memguard.ErrLowMemory):
		reply(&mimirv1.TrainResult{LowMemory: true, Error: lowMemoryReply})
	case ctx.Err() != nil:
		reply(&mimirv1.TrainResult{Cancelled: true})
	case err != nil:
		reply(&mimirv1.TrainResult{Error: err.Error()})
	case t.GroupSize > 1 && t.GroupRank != 0:
		reply(&mimirv1.TrainResult{}) // member 0 sends the weights
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
