// Package cluster keeps the links to connected workers and hands them
// predictions and training tasks.
package cluster

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
)

// ErrLinkLost means the worker went away while it had a task. A training
// run that loses its worker goes back in the queue.
var ErrLinkLost = training.ErrWorkerLost

// ErrWorkerFailed carries an error a worker reported for a task.
var ErrWorkerFailed = errors.New("cluster: the worker couldn't do it")

// helloTimeout is how long a new link has to say hello, and then to
// answer the challenge.
const helloTimeout = 30 * time.Second

// sampleEvery is how often a worker's stats are kept for its charts.
const sampleEvery = time.Minute

// ProofContext starts the message a worker signs to answer a challenge.
const ProofContext = "mimir-worker-link-v1\n"

// ErrBadProof means a worker couldn't prove it holds its key.
var ErrBadProof = errors.New("cluster: the worker's key didn't check out")

// defaultCapacity is how many tasks a worker takes at once when it
// doesn't say.
const defaultCapacity = 4

// DefaultIdleTimeout is how long a link may stay silent before the pool
// takes the worker for dead: three missed heartbeats (workers send one
// every 15 seconds).
const DefaultIdleTimeout = 45 * time.Second

// Pool is the set of connected workers.
type Pool struct {
	// OnSample, when set, is given a worker's stats about once a minute.
	OnSample func(workerID string, s store.WorkerSample)
	// IdleTimeout is how long a link may go without a message, heartbeat
	// or reply, before it is dropped and its tasks fail with ErrLinkLost.
	// Zero means DefaultIdleTimeout. Set it before the first Serve.
	IdleTimeout time.Duration
	// OnLink, when set, is called on its own goroutine once a worker's
	// link is up, for example to hand it the models placed on it.
	OnLink func(workerID string)

	log      *slog.Logger
	mu       sync.Mutex
	links    map[string]*link
	freed    chan struct{} // closed and replaced whenever a slot may have come free
	seq      int64
	stop     chan struct{} // closed by Close
	stopOnce sync.Once
}

// Join is who is on the other end of a new link.
type Join struct {
	Worker store.Worker
	Addr   string
	// Security and TLSVersion say how the link is protected, for display.
	Security   string
	TLSVersion string
	// Verify is given the worker's hello once it has signed the challenge
	// with the key in it. It decides whether that key may join as Worker.
	Verify func(ctx context.Context, hello *mimirv1.Hello) error
}

// Task is something a worker is doing.
type Task struct {
	Kind    string // "predict" or "train"
	ModelID string
	JobID   string
	Started time.Time
}

// New returns an empty pool.
func New(log *slog.Logger) *Pool {
	return &Pool{log: log, links: map[string]*link{}, freed: make(chan struct{}), stop: make(chan struct{})}
}

// Close ends every link and refuses new ones, for a server shutting down.
// A link otherwise never ends on its own, so http.Server.Shutdown would
// wait out its whole timeout whenever a worker is connected. Workers
// reconnect once the server is back.
func (p *Pool) Close() {
	p.stopOnce.Do(func() { close(p.stop) })
}

// Status is what the pool knows about one connected worker.
type Status struct {
	Worker     store.Worker
	Hello      *mimirv1.Hello
	Addr       string
	Busy       int
	Since      time.Time
	Stats      *mimirv1.Stats
	Tasks      []Task
	Security   string
	TLSVersion string
	// Trains and TrainingOnly are what the worker takes now: what it
	// said in its hello, narrowed by what the console told it to take.
	Trains       bool
	TrainingOnly bool
	// Loaded is the version of each model the link holds, by model id.
	Loaded map[string]string
}

type link struct {
	id     string
	pool   *Pool // nil in tests that build links by hand
	worker store.Worker
	hello  *mimirv1.Hello
	// wants is what the console told the worker to take ("", "all",
	// "train" or "serve"); guarded by Pool.mu.
	wants  string
	join   Join
	since  time.Time
	stream *connect.BidiStream[mimirv1.LinkRequest, mimirv1.LinkResponse]

	sendMu sync.Mutex // one message at a time down the stream
	ended  bool       // the stream is done; guarded by sendMu

	mu      sync.Mutex
	busy    int
	seq     int64
	sent    map[string]string // model id to the version of its weights sent down this link
	loading map[string]string // model id to the version on its way down this link, from Warm
	pending map[string]chan *mimirv1.LinkRequest
	tasks   map[string]Task
	stats   *mimirv1.Stats
	sampled time.Time
	closed  chan struct{}
	dropped chan struct{} // closed when the worker is deleted
}

// trains reports whether the link takes training runs: the worker trains
// and the console hasn't limited it to answering. Call with Pool.mu held.
func (l *link) trains() bool { return l.hello.Trains && l.wants != "serve" }

// trainingOnly reports whether the link takes no predictions: the worker
// said so, or the console limited it to training. Call with Pool.mu held.
func (l *link) trainingOnly() bool { return l.hello.TrainingOnly || l.wants == "train" }

func (l *link) drop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.dropped:
	default:
		close(l.dropped)
	}
}

// Serve runs a worker's link until the stream ends or ctx is done. It
// returns the error to close the stream with.
func (p *Pool) Serve(ctx context.Context, stream *connect.BidiStream[mimirv1.LinkRequest, mimirv1.LinkResponse], j Join) error {
	w, addr := j.Worker, j.Addr
	select {
	case <-p.stop:
		return connect.NewError(connect.CodeUnavailable, errors.New("The server is shutting down."))
	default:
	}
	msg, err := receive(ctx, stream)
	if err != nil {
		return err
	}
	hello := msg.GetHello()
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("A worker starts its link with a hello."))
	}
	if hello.Capacity <= 0 {
		hello.Capacity = defaultCapacity
	}
	if err := p.challenge(ctx, stream, hello); err != nil {
		p.log.WarnContext(ctx, "worker: refused", "worker", w.Name, "addr", addr, "error", err)
		return connect.NewError(connect.CodePermissionDenied, errors.New("This worker couldn't prove it holds its key. Update the worker to this version."))
	}
	if j.Verify != nil {
		if err := j.Verify(ctx, hello); err != nil {
			return err
		}
	}

	l := &link{pool: p, worker: w, hello: hello, join: j, wants: w.Tasks, since: time.Now(), stream: stream, sent: map[string]string{}, loading: map[string]string{}, pending: map[string]chan *mimirv1.LinkRequest{}, tasks: map[string]Task{}, closed: make(chan struct{}), dropped: make(chan struct{})}
	l.note(p, hello.Stats)
	p.mu.Lock()
	p.seq++
	l.id = fmt.Sprint(p.seq)
	p.links[l.id] = l
	p.wake()
	p.mu.Unlock()
	defer func() {
		// No send may touch the stream once Serve returns: connect
		// flushes and closes it from here on.
		l.sendMu.Lock()
		l.ended = true
		l.sendMu.Unlock()
		p.mu.Lock()
		delete(p.links, l.id)
		p.mu.Unlock()
		close(l.closed)
	}()
	log := p.log.With("worker", w.Name, "addr", addr, "security", j.Security)
	log.InfoContext(ctx, "worker: connected", "device", hello.Device, "architectures", hello.Architectures, "trains", hello.Trains, "training_only", hello.TrainingOnly, "tasks", w.Tasks)
	defer log.InfoContext(ctx, "worker: disconnected")

	if err := l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Welcome{Welcome: &mimirv1.Welcome{WorkerId: w.ID, Name: w.Name, Tasks: w.Tasks}}}); err != nil {
		return err
	}
	if p.OnLink != nil {
		go p.OnLink(w.ID)
	}
	// Receive blocks until the worker says something or the stream ends,
	// so it runs on its own goroutine and the loop below can give up on a
	// worker that has gone quiet. Once Serve returns, connect ends the
	// stream and the pending Receive returns.
	msgs := make(chan received)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			msg, err := stream.Receive()
			select {
			case msgs <- received{msg, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	idle := p.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	quiet := time.NewTimer(idle)
	defer quiet.Stop()
	for {
		var r received
		select {
		case r = <-msgs:
		case <-quiet.C:
			log.WarnContext(ctx, "worker: no heartbeat, dropping the link", "idle", idle)
			return connect.NewError(connect.CodeDeadlineExceeded, errors.New("The worker stopped sending heartbeats."))
		case <-l.dropped:
			return connect.NewError(connect.CodeUnauthenticated, errors.New("This worker was deleted."))
		case <-p.stop:
			log.InfoContext(ctx, "worker: ending the link for shutdown")
			return nil
		case <-ctx.Done():
			return nil
		}
		msg, err := r.msg, r.err
		if errors.Is(err, io.EOF) || ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		quiet.Reset(idle)
		select {
		case <-l.dropped:
			return connect.NewError(connect.CodeUnauthenticated, errors.New("This worker was deleted."))
		default:
		}
		var taskID string
		switch b := msg.Body.(type) {
		case *mimirv1.LinkRequest_Heartbeat:
			l.note(p, b.Heartbeat.GetStats())
			continue
		case *mimirv1.LinkRequest_PredictResult:
			taskID = b.PredictResult.GetTaskId()
		case *mimirv1.LinkRequest_TrainProgress:
			taskID = b.TrainProgress.GetTaskId()
		case *mimirv1.LinkRequest_TrainResult:
			taskID = b.TrainResult.GetTaskId()
		case *mimirv1.LinkRequest_LoadResult:
			taskID = b.LoadResult.GetTaskId()
		default:
			continue // a second hello
		}
		l.mu.Lock()
		ch := l.pending[taskID]
		l.mu.Unlock()
		if ch == nil {
			continue // a reply to a task nobody waits for anymore
		}
		select {
		case ch <- msg:
		case <-ctx.Done():
			return nil
		}
	}
}

// received is one Receive's result.
type received struct {
	msg *mimirv1.LinkRequest
	err error
}

// receive waits for the next message, for at most helloTimeout.
func receive(ctx context.Context, stream *connect.BidiStream[mimirv1.LinkRequest, mimirv1.LinkResponse]) (*mimirv1.LinkRequest, error) {
	got := make(chan *mimirv1.LinkRequest, 1)
	go func() {
		msg, err := stream.Receive()
		if err != nil {
			close(got)
			return
		}
		got <- msg
	}()
	select {
	case msg, ok := <-got:
		if !ok {
			return nil, connect.NewError(connect.CodeCanceled, errors.New("The worker hung up."))
		}
		return msg, nil
	case <-time.After(helloTimeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("The worker took too long to answer."))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// challenge asks the worker to sign a fresh nonce with the key in its
// hello, so a copied hello or a stolen token alone can't join.
func (p *Pool) challenge(ctx context.Context, stream *connect.BidiStream[mimirv1.LinkRequest, mimirv1.LinkResponse], hello *mimirv1.Hello) error {
	if len(hello.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: no key in its hello", ErrBadProof)
	}
	nonce := make([]byte, 32)
	rand.Read(nonce)
	if err := stream.Send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Challenge{Challenge: &mimirv1.Challenge{Nonce: nonce}}}); err != nil {
		return err
	}
	msg, err := receive(ctx, stream)
	if err != nil {
		return err
	}
	sig := msg.GetProof().GetSignature()
	if !ed25519.Verify(hello.PublicKey, append([]byte(ProofContext), nonce...), sig) {
		return fmt.Errorf("%w: the signature doesn't match", ErrBadProof)
	}
	return nil
}

// note keeps a worker's latest stats, and hands one to OnSample a minute.
func (l *link) note(p *Pool, st *mimirv1.Stats) {
	if st == nil {
		return
	}
	now := time.Now()
	l.mu.Lock()
	l.stats = st
	take := now.Sub(l.sampled) >= sampleEvery
	if take {
		l.sampled = now
	}
	busy := l.busy
	l.mu.Unlock()
	if take && p.OnSample != nil {
		p.OnSample(l.worker.ID, store.WorkerSample{
			Time: now, CPUPercent: st.CpuPercent,
			MemoryUsed: st.MemoryUsedBytes, MemoryTotal: st.MemoryTotalBytes,
			DiskUsed: st.DiskUsedBytes, DiskTotal: st.DiskTotalBytes,
			Busy: busy, Capacity: int(l.hello.Capacity),
		})
	}
}

func (l *link) send(msg *mimirv1.LinkResponse) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	if l.ended {
		return ErrLinkLost
	}
	return l.stream.Send(msg)
}

// open registers a task and returns its id and reply channel. l.mu must
// be held, and the caller must have checked the link has room.
func (l *link) open(t Task) (string, chan *mimirv1.LinkRequest) {
	l.seq++
	id := fmt.Sprintf("%s-%d", l.id, l.seq)
	ch := make(chan *mimirv1.LinkRequest, 16)
	l.pending[id] = ch
	t.Started = time.Now()
	l.tasks[id] = t
	l.busy++
	return id, ch
}

func (l *link) finish(id string) {
	l.mu.Lock()
	delete(l.pending, id)
	delete(l.tasks, id)
	l.busy--
	l.mu.Unlock()
	if l.pool != nil {
		l.pool.mu.Lock()
		l.pool.wake()
		l.pool.mu.Unlock()
	}
}

// wake tells predictions waiting for a slot to try again. p.mu must be
// held.
func (p *Pool) wake() {
	close(p.freed)
	p.freed = make(chan struct{})
}

// able reports whether some link could do the job if it had room: the
// difference between "wait" and "nobody can". With a workerID only that
// worker's links count.
func (p *Pool) able(archName string, train bool, workerID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		if workerID != "" && l.worker.ID != workerID {
			continue
		}
		if slices.Contains(l.hello.Architectures, archName) && (!train || l.trains()) && (train || !l.trainingOnly()) {
			return true
		}
	}
	return false
}

// reserveWait is reserve that waits for a slot while every link that
// could do the job is full, so a burst of predictions queues rather than
// fails. It returns no link when nobody can do the job, and ctx's error
// when the wait ran out.
func (p *Pool) reserveWait(ctx context.Context, archName string, train bool, workerID string, t Task) (*link, string, chan *mimirv1.LinkRequest, error) {
	for {
		p.mu.Lock()
		freed := p.freed
		p.mu.Unlock()
		l, id, ch := p.reserve(archName, train, workerID, t)
		if l != nil {
			return l, id, ch, nil
		}
		if !p.able(archName, train, workerID) {
			return nil, "", nil, nil
		}
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, "", nil, ctx.Err()
		}
	}
}

// listen is open for a message that is no task: it takes no slot and
// shows on no dashboard, only a channel for the reply. Close with hangUp.
func (l *link) listen() (string, chan *mimirv1.LinkRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	id := fmt.Sprintf("%s-%d", l.id, l.seq)
	ch := make(chan *mimirv1.LinkRequest, 1)
	l.pending[id] = ch
	return id, ch
}

func (l *link) hangUp(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.pending, id)
}

// pick returns the least busy link that can do the job and has room, or
// nil. With a workerID only that worker's links count.
func (p *Pool) pick(archName string, train bool, workerID string) *link {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.best(archName, train, workerID)
}

// Memory pressure as a worker reports it in its stats (see memguard).
const (
	pressureHigh     = 1
	pressureCritical = 2
)

// best is pick with p.mu held. A worker whose memory is running short
// takes no training runs, and gets a prediction only when every other
// worker that could run it is as short.
func (p *Pool) best(archName string, train bool, workerID string) *link {
	var best *link
	bestBusy, bestShort := 0, false
	for _, l := range p.links {
		if workerID != "" && l.worker.ID != workerID {
			continue
		}
		if !slices.Contains(l.hello.Architectures, archName) || (train && !l.trains()) || (!train && l.trainingOnly()) {
			continue
		}
		l.mu.Lock()
		busy, cap := l.busy, int(l.hello.Capacity)
		pressure := l.stats.GetMemoryPressure()
		l.mu.Unlock()
		if busy >= cap || (train && pressure >= pressureHigh) {
			continue
		}
		short := pressure >= pressureCritical
		if best == nil || (bestShort && !short) || (short == bestShort && busy < bestBusy) {
			best, bestBusy, bestShort = l, busy, short
		}
	}
	return best
}

// reserve picks a link like pick and takes a slot on it for t in the same
// critical section, so two callers can't both take a link's last slot. It
// returns nil when no link has room; otherwise the caller must finish the
// task id.
func (p *Pool) reserve(archName string, train bool, workerID string, t Task) (*link, string, chan *mimirv1.LinkRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.best(archName, train, workerID)
	if l == nil {
		return nil, "", nil
	}
	// Slots are only taken here, under p.mu, and finish only gives them
	// back, so the room best saw is still there.
	l.mu.Lock()
	id, ch := l.open(t)
	l.mu.Unlock()
	return l, id, ch
}

// Drop closes every link of a worker, for example after it is deleted.
// The stream ends when its handler returns, so the link is just forgotten
// here and its handler sees the worker gone on the next message.
func (p *Pool) Drop(workerID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, l := range p.links {
		if l.worker.ID == workerID {
			delete(p.links, id)
			l.drop()
		}
	}
}

// CanPredict reports whether a connected worker runs architecture a; with
// a workerID, whether that worker does.
func (p *Pool) CanPredict(a, workerID string) bool { return p.pick(a, false, workerID) != nil }

// CanTrain reports whether a connected worker trains architecture a; with
// a workerID, whether that worker does.
func (p *Pool) CanTrain(a, workerID string) bool { return p.pick(a, true, workerID) != nil }

// Online reports whether a worker has a link open.
func (p *Pool) Online(workerID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		if l.worker.ID == workerID {
			return true
		}
	}
	return false
}

// Statuses describes every connected worker.
func (p *Pool) Statuses() []Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Status, 0, len(p.links))
	for _, l := range p.links {
		l.mu.Lock()
		st := Status{Worker: l.worker, Hello: l.hello, Addr: l.join.Addr, Busy: l.busy, Since: l.since, Stats: l.stats, Security: l.join.Security, TLSVersion: l.join.TLSVersion, Trains: l.trains(), TrainingOnly: l.trainingOnly(), Loaded: maps.Clone(l.sent)}
		for _, t := range l.tasks {
			st.Tasks = append(st.Tasks, t)
		}
		out = append(out, st)
		l.mu.Unlock()
	}
	return out
}

// Predict runs input through version ver of a model on a worker. ran is
// false when no worker can run the architecture.
func (p *Pool) Predict(ctx context.Context, modelID, ver, workerID string, a arch.Architecture, spec arch.Spec, load func() ([]byte, error), input []float32) (res runtime.Result, worker string, ran bool, err error) {
	l, id, replies, err := p.reserveWait(ctx, a.Name, false, workerID, Task{Kind: "predict", ModelID: modelID})
	if err != nil {
		return res, "", true, err // a worker could have, but the wait ran out
	}
	if l == nil {
		return res, "", false, nil
	}
	defer l.finish(id)
	task := &mimirv1.PredictTask{
		TaskId: id, ModelId: modelID, Architecture: a.Name,
		Spec:  &mimirv1.ModelSpec{InputShape: int64s(spec.InputShape), Labels: spec.Labels},
		Input: &mimirv1.Tensor{Shape: int64s(spec.InputShape), Values: input},
	}
	// The weights ride along when the link hasn't got this version, unless
	// Warm is sending them now: then the worker answers with the version
	// it has, or asks, rather than loading the same weights twice.
	l.mu.Lock()
	need := l.sent[modelID] != ver && l.loading[modelID] != ver
	l.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if need {
			if task.Weights, err = load(); err != nil {
				return res, l.worker.Name, true, err
			}
		}
		if err := l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Predict{Predict: task}}); err != nil {
			return res, l.worker.Name, true, ErrLinkLost
		}
		if need {
			l.mu.Lock()
			l.sent[modelID] = ver
			l.mu.Unlock()
		}
		msg, err := wait(ctx, replies, l.closed)
		if err != nil {
			if !errors.Is(err, ErrLinkLost) {
				// The caller gave up or ran out of time: tell the worker
				// so it stops working on an answer nobody will read.
				l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Cancel{Cancel: &mimirv1.CancelTask{TaskId: id}}})
			}
			return res, l.worker.Name, true, err
		}
		r := msg.GetPredictResult()
		switch {
		case r == nil:
			return res, l.worker.Name, true, fmt.Errorf("%w: unexpected reply", ErrWorkerFailed)
		case r.NeedWeights && !need:
			need = true // it lost them; send again with the weights
			continue
		case r.Error != "":
			return res, l.worker.Name, true, fmt.Errorf("%w: %s", ErrWorkerFailed, r.Error)
		}
		return runtime.Result{Output: r.GetOutput().GetValues(), Probability: r.GetProbabilities().GetValues()}, l.worker.Name, true, nil
	}
	return res, l.worker.Name, true, fmt.Errorf("%w: it keeps asking for the weights", ErrWorkerFailed)
}

// Warm hands version ver of a model to every connected worker that can
// answer for it (with a workerID, that worker only) and doesn't hold it
// yet, so the first prediction after a run finishes is as quick as the
// rest. A worker keeps answering with the version it has until the new
// one is loaded. It returns how many workers took the weights and the
// first failure, which leaves that worker to fetch them on its first
// prediction as before.
func (p *Pool) Warm(ctx context.Context, modelID, ver, workerID string, a arch.Architecture, spec arch.Spec, load func() ([]byte, error)) (int, error) {
	var links []*link
	p.mu.Lock()
	for _, l := range p.links {
		if workerID != "" && l.worker.ID != workerID {
			continue
		}
		if !slices.Contains(l.hello.Architectures, a.Name) || l.trainingOnly() {
			continue
		}
		l.mu.Lock()
		if l.sent[modelID] != ver && l.loading[modelID] != ver {
			l.loading[modelID] = ver
			links = append(links, l)
		}
		l.mu.Unlock()
	}
	p.mu.Unlock()
	if len(links) == 0 {
		return 0, nil
	}
	done := func(l *link, ok bool) {
		l.mu.Lock()
		if l.loading[modelID] == ver {
			delete(l.loading, modelID)
		}
		if ok {
			l.sent[modelID] = ver
		}
		l.mu.Unlock()
	}
	weights, err := load()
	if err != nil {
		for _, l := range links {
			done(l, false)
		}
		return 0, err
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		n     int
		first error
	)
	for _, l := range links {
		wg.Go(func() {
			err := p.warm(ctx, l, modelID, a, spec, weights)
			done(l, err == nil)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				n++
			} else if first == nil {
				first = fmt.Errorf("%s: %w", l.worker.Name, err)
			}
		})
	}
	wg.Wait()
	return n, first
}

// warm sends one link a model's weights and waits for it to load them.
func (p *Pool) warm(ctx context.Context, l *link, modelID string, a arch.Architecture, spec arch.Spec, weights []byte) error {
	id, replies := l.listen()
	defer l.hangUp(id)
	task := &mimirv1.LoadTask{TaskId: id, ModelId: modelID, Architecture: a.Name, Spec: &mimirv1.ModelSpec{InputShape: int64s(spec.InputShape), Labels: spec.Labels}, Weights: weights}
	if err := l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Load{Load: task}}); err != nil {
		return ErrLinkLost
	}
	msg, err := wait(ctx, replies, l.closed)
	if err != nil {
		return err
	}
	r := msg.GetLoadResult()
	switch {
	case r == nil:
		return fmt.Errorf("%w: unexpected reply", ErrWorkerFailed)
	case r.Error != "":
		return fmt.Errorf("%w: %s", ErrWorkerFailed, r.Error)
	}
	return nil
}

// Train runs a training task on a worker, reporting progress as it goes.
// ran is false when no worker can train the architecture. When ctx ends
// the worker is told to stop and the error is ctx's cause.
func (p *Pool) Train(ctx context.Context, jobID, workerID string, t training.Task, progress training.Progress) (res training.Result, worker string, ran bool, err error) {
	l, id, replies := p.reserve(t.Arch.Name, true, workerID, Task{Kind: "train", JobID: jobID})
	if l == nil {
		return res, "", false, nil
	}
	defer l.finish(id)
	task := &mimirv1.TrainTask{
		TaskId: id, JobId: jobID, Architecture: t.Arch.Name,
		Settings:    &mimirv1.TrainingSettings{Epochs: int32(t.Settings.Epochs), BatchSize: int32(t.Settings.BatchSize), LearningRate: t.Settings.LearningRate, HoldOut: t.Settings.HoldOut, Layers: LayersToProto(t.Settings.Layers)},
		DatasetKind: mimirv1.DatasetKind(t.Source.Kind),
		Target:      t.Source.Target, Columns: ColumnsToProto(t.Source.Columns),
		History:     EpochsToProto(t.History),
		BaseWeights: t.BaseWeights, BaseLabels: t.BaseLabels, BaseFeatures: t.BaseFeatures,
	}
	if t.Source.Kind.Archive() {
		task.Archive = t.Source.CSV
	} else {
		task.Csv = t.Source.CSV
	}
	if err := l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Train{Train: task}}); err != nil {
		return res, l.worker.Name, true, ErrLinkLost
	}
	cancelled := false
	for {
		msg, err := wait(ctx, replies, l.closed)
		if errors.Is(err, ErrLinkLost) {
			return res, l.worker.Name, true, err
		}
		if err != nil && !cancelled {
			// ctx ended: ask the worker to stop, then wait for its last word.
			cancelled = true
			l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Cancel{Cancel: &mimirv1.CancelTask{TaskId: id}}})
			grace, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			msg, err = wait(grace, replies, l.closed)
			stop()
			if err != nil {
				return res, l.worker.Name, true, context.Cause(ctx)
			}
		} else if err != nil {
			return res, l.worker.Name, true, context.Cause(ctx)
		}
		switch b := msg.Body.(type) {
		case *mimirv1.LinkRequest_TrainProgress:
			if progress != nil && !cancelled {
				pr := b.TrainProgress
				progress(int(pr.Epoch), int(pr.Step), int(pr.StepsPerEpoch), EpochsFromProto(pr.History))
			}
		case *mimirv1.LinkRequest_TrainResult:
			r := b.TrainResult
			switch {
			case r.LowMemory && !cancelled:
				return res, l.worker.Name, true, fmt.Errorf("%w on %s", training.ErrLowMemory, l.worker.Name)
			case cancelled || r.Cancelled:
				return res, l.worker.Name, true, cmpCause(ctx)
			case r.Error != "":
				return res, l.worker.Name, true, fmt.Errorf("%w: %s", ErrWorkerFailed, r.Error)
			}
			sp := r.GetSpec()
			return training.Result{
				Weights:  r.Weights,
				History:  EpochsFromProto(r.History),
				Spec:     arch.Spec{InputShape: ints(sp.GetInputShape()), Labels: sp.GetLabels()},
				Features: sp.GetFeatures(),
				Columns:  ColumnsFromProto(sp.GetColumns()),
				Fresh:    r.Fresh,
			}, l.worker.Name, true, nil
		}
	}
}

func cmpCause(ctx context.Context) error {
	if c := context.Cause(ctx); c != nil {
		return c
	}
	return fmt.Errorf("%w: the worker stopped the task", ErrWorkerFailed)
}

// wait returns the next reply, or why there won't be one.
func wait(ctx context.Context, replies chan *mimirv1.LinkRequest, closed chan struct{}) (*mimirv1.LinkRequest, error) {
	select {
	case msg := <-replies:
		return msg, nil
	case <-closed:
		return nil, ErrLinkLost
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// LayersToProto converts a designed model's layers, as arch.FormatLayers
// writes them, for the wire.
func LayersToProto(s string) []*mimirv1.Layer {
	ls, _ := arch.ParseLayers(s) // checked when the run was made
	out := make([]*mimirv1.Layer, len(ls))
	for i, l := range ls {
		out[i] = &mimirv1.Layer{Kind: l.Kind, Units: int32(l.Units), Rate: l.Rate}
	}
	return out
}

// EpochsToProto converts a job's history.
func EpochsToProto(h []store.Epoch) []*mimirv1.EpochResult {
	out := make([]*mimirv1.EpochResult, len(h))
	for i, e := range h {
		out[i] = &mimirv1.EpochResult{Epoch: int32(e.Epoch), Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, DurationMillis: e.Millis, TypicalError: e.MAE}
	}
	return out
}

// ColumnsToProto converts table columns for the wire.
func ColumnsToProto(cols []schema.Column) []*mimirv1.Column {
	out := make([]*mimirv1.Column, len(cols))
	for i, c := range cols {
		out[i] = &mimirv1.Column{Name: c.Name, Kind: string(c.Kind), Values: c.Values, Fill: c.Fill}
	}
	return out
}

// ColumnsFromProto converts table columns back.
func ColumnsFromProto(cols []*mimirv1.Column) []schema.Column {
	out := make([]schema.Column, len(cols))
	for i, c := range cols {
		out[i] = schema.Column{Name: c.GetName(), Kind: schema.Kind(c.GetKind()), Values: c.GetValues(), Fill: c.GetFill()}
	}
	return out
}

// EpochsFromProto converts a history back.
func EpochsFromProto(h []*mimirv1.EpochResult) []store.Epoch {
	out := make([]store.Epoch, len(h))
	for i, e := range h {
		out[i] = store.Epoch{Epoch: int(e.Epoch), Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, Millis: e.DurationMillis, MAE: e.TypicalError}
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

func ints(v []int64) []int {
	out := make([]int, len(v))
	for i, x := range v {
		out[i] = int(x)
	}
	return out
}
