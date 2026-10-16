// Package cluster keeps the links to connected workers and hands them
// predictions and training tasks.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
)

// ErrLinkLost means the worker went away while it had a task.
var ErrLinkLost = errors.New("cluster: the worker disconnected")

// ErrWorkerFailed carries an error a worker reported for a task.
var ErrWorkerFailed = errors.New("cluster: the worker couldn't do it")

// helloTimeout is how long a new link has to say hello.
const helloTimeout = 30 * time.Second

// defaultCapacity is how many tasks a worker takes at once when it
// doesn't say.
const defaultCapacity = 4

// Pool is the set of connected workers.
type Pool struct {
	log   *slog.Logger
	mu    sync.Mutex
	links map[string]*link
	seq   int64
}

// New returns an empty pool.
func New(log *slog.Logger) *Pool {
	return &Pool{log: log, links: map[string]*link{}}
}

// Status is what the pool knows about one connected worker.
type Status struct {
	Worker store.Worker
	Hello  *mimirv1.Hello
	Addr   string
	Busy   int
	Since  time.Time
}

type link struct {
	id     string
	worker store.Worker
	hello  *mimirv1.Hello
	addr   string
	since  time.Time
	stream *connect.BidiStream[mimirv1.LinkRequest, mimirv1.LinkResponse]

	sendMu sync.Mutex // one message at a time down the stream

	mu      sync.Mutex
	busy    int
	seq     int64
	sent    map[string]bool // model ids whose weights went down this link
	pending map[string]chan *mimirv1.LinkRequest
	closed  chan struct{}
	dropped chan struct{} // closed when the worker is deleted
}

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
func (p *Pool) Serve(ctx context.Context, stream *connect.BidiStream[mimirv1.LinkRequest, mimirv1.LinkResponse], w store.Worker, addr string) error {
	first := make(chan *mimirv1.LinkRequest, 1)
	go func() {
		msg, err := stream.Receive()
		if err != nil {
			close(first)
			return
		}
		first <- msg
	}()
	var hello *mimirv1.Hello
	select {
	case msg, ok := <-first:
		if ok {
			hello = msg.GetHello()
		}
	case <-time.After(helloTimeout):
	case <-ctx.Done():
		return ctx.Err()
	}
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("A worker starts its link with a hello."))
	}
	if hello.Capacity <= 0 {
		hello.Capacity = defaultCapacity
	}

	l := &link{worker: w, hello: hello, addr: addr, since: time.Now(), stream: stream, sent: map[string]bool{}, pending: map[string]chan *mimirv1.LinkRequest{}, closed: make(chan struct{}), dropped: make(chan struct{})}
	p.mu.Lock()
	p.seq++
	l.id = fmt.Sprint(p.seq)
	p.links[l.id] = l
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.links, l.id)
		p.mu.Unlock()
		close(l.closed)
	}()
	log := p.log.With("worker", w.Name, "addr", addr)
	log.InfoContext(ctx, "worker: connected", "device", hello.Device, "architectures", hello.Architectures, "trains", hello.Trains)
	defer log.InfoContext(ctx, "worker: disconnected")

	if err := l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Welcome{Welcome: &mimirv1.Welcome{WorkerId: w.ID, Name: w.Name}}}); err != nil {
		return err
	}
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) || ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-l.dropped:
			return connect.NewError(connect.CodeUnauthenticated, errors.New("This worker was deleted."))
		default:
		}
		var taskID string
		switch b := msg.Body.(type) {
		case *mimirv1.LinkRequest_PredictResult:
			taskID = b.PredictResult.GetTaskId()
		case *mimirv1.LinkRequest_TrainProgress:
			taskID = b.TrainProgress.GetTaskId()
		case *mimirv1.LinkRequest_TrainResult:
			taskID = b.TrainResult.GetTaskId()
		default:
			continue // heartbeat, or a second hello
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

func (l *link) send(msg *mimirv1.LinkResponse) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	return l.stream.Send(msg)
}

// open registers a task and returns its id and reply channel.
func (l *link) open() (string, chan *mimirv1.LinkRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	id := fmt.Sprintf("%s-%d", l.id, l.seq)
	ch := make(chan *mimirv1.LinkRequest, 16)
	l.pending[id] = ch
	l.busy++
	return id, ch
}

func (l *link) finish(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.pending, id)
	l.busy--
}

// pick returns the least busy link that can do the job, or nil.
func (p *Pool) pick(archName string, train bool) *link {
	p.mu.Lock()
	defer p.mu.Unlock()
	var best *link
	for _, l := range p.links {
		if !slices.Contains(l.hello.Architectures, archName) || (train && !l.hello.Trains) {
			continue
		}
		l.mu.Lock()
		busy, cap := l.busy, int(l.hello.Capacity)
		l.mu.Unlock()
		if busy >= cap {
			continue
		}
		if best == nil || busy < best.busy {
			best = l
		}
	}
	return best
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

// CanPredict reports whether a connected worker runs architecture a.
func (p *Pool) CanPredict(a string) bool { return p.pick(a, false) != nil }

// CanTrain reports whether a connected worker trains architecture a.
func (p *Pool) CanTrain(a string) bool { return p.pick(a, true) != nil }

// Statuses describes every connected worker.
func (p *Pool) Statuses() []Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Status, 0, len(p.links))
	for _, l := range p.links {
		l.mu.Lock()
		out = append(out, Status{Worker: l.worker, Hello: l.hello, Addr: l.addr, Busy: l.busy, Since: l.since})
		l.mu.Unlock()
	}
	return out
}

// Predict runs input through a model on a worker. ran is false when no
// worker can run the architecture.
func (p *Pool) Predict(ctx context.Context, modelID string, a arch.Architecture, spec arch.Spec, load func() ([]byte, error), input []float32) (res runtime.Result, worker string, ran bool, err error) {
	l := p.pick(a.Name, false)
	if l == nil {
		return res, "", false, nil
	}
	id, replies := l.open()
	defer l.finish(id)
	task := &mimirv1.PredictTask{
		TaskId: id, ModelId: modelID, Architecture: a.Name,
		Spec:  &mimirv1.ModelSpec{InputShape: int64s(spec.InputShape), Labels: spec.Labels},
		Input: &mimirv1.Tensor{Shape: int64s(spec.InputShape), Values: input},
	}
	l.mu.Lock()
	need := !l.sent[modelID]
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
			l.sent[modelID] = true
			l.mu.Unlock()
		}
		msg, err := wait(ctx, replies, l.closed)
		if err != nil {
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

// Train runs a training task on a worker, reporting progress as it goes.
// ran is false when no worker can train the architecture. When ctx ends
// the worker is told to stop and the error is ctx's cause.
func (p *Pool) Train(ctx context.Context, jobID string, t training.Task, progress training.Progress) (res training.Result, worker string, ran bool, err error) {
	l := p.pick(t.Arch.Name, true)
	if l == nil {
		return res, "", false, nil
	}
	id, replies := l.open()
	defer l.finish(id)
	task := &mimirv1.TrainTask{
		TaskId: id, JobId: jobID, Architecture: t.Arch.Name,
		Settings:    &mimirv1.TrainingSettings{Epochs: int32(t.Settings.Epochs), BatchSize: int32(t.Settings.BatchSize), LearningRate: t.Settings.LearningRate, HoldOut: t.Settings.HoldOut},
		DatasetKind: mimirv1.DatasetKind(t.Source.Kind),
		Csv:         t.Source.CSV, Target: t.Source.Target,
		History: EpochsToProto(t.History),
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

// EpochsToProto converts a job's history.
func EpochsToProto(h []store.Epoch) []*mimirv1.EpochResult {
	out := make([]*mimirv1.EpochResult, len(h))
	for i, e := range h {
		out[i] = &mimirv1.EpochResult{Epoch: int32(e.Epoch), Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, DurationMillis: e.Millis}
	}
	return out
}

// EpochsFromProto converts a history back.
func EpochsFromProto(h []*mimirv1.EpochResult) []store.Epoch {
	out := make([]store.Epoch, len(h))
	for i, e := range h {
		out[i] = store.Epoch{Epoch: int(e.Epoch), Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, Millis: e.DurationMillis}
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
