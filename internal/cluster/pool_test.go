package cluster

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// linkService serves the pool's Link and nothing else, joining every
// link as worker.
type linkService struct {
	mimirv1connect.UnimplementedWorkerServiceHandler
	pool   *Pool
	worker store.Worker
}

func (s linkService) Link(ctx context.Context, stream *connect.BidiStream[mimirv1.LinkRequest, mimirv1.LinkResponse]) error {
	return s.pool.Serve(ctx, stream, Join{Worker: s.worker, Addr: "test"})
}

// silentWorker joins p over a real gRPC stream, then says nothing more, as
// a worker does whose machine hung or whose network vanished. It returns
// the client stream and the messages the pool sent after the welcome.
func silentWorker(t *testing.T, p *Pool) (*connect.BidiStreamForClient[mimirv1.LinkRequest, mimirv1.LinkResponse], <-chan *mimirv1.LinkResponse) {
	t.Helper()
	return scriptedWorker(t, p, store.Worker{ID: "w1", Name: "silent"}, &mimirv1.Hello{Architectures: []string{"table-mlp"}, Capacity: 1})
}

// scriptedWorker joins p as w with hello over a real gRPC stream, for a
// test to speak for the worker. It returns the client stream and the
// messages the pool sent after the welcome.
func scriptedWorker(t *testing.T, p *Pool, w store.Worker, hello *mimirv1.Hello) (*connect.BidiStreamForClient[mimirv1.LinkRequest, mimirv1.LinkResponse], <-chan *mimirv1.LinkResponse) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(mimirv1connect.NewWorkerServiceHandler(linkService{pool: p, worker: w}))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)

	h2c := new(http.Protocols)
	h2c.SetUnencryptedHTTP2(true)
	client := mimirv1connect.NewWorkerServiceClient(&http.Client{Transport: &http.Transport{Protocols: h2c}}, srv.URL, connect.WithGRPC())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream := client.Link(ctx)
	t.Cleanup(func() { // a blocked Receive only wakes when the stream closes
		stream.CloseRequest()
		stream.CloseResponse()
	})
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	hello.PublicKey = pub
	if err := stream.Send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Hello{Hello: hello}}); err != nil {
		t.Fatal(err)
	}
	ch, err := stream.Receive()
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(key, append([]byte(ProofContext), ch.GetChallenge().GetNonce()...))
	if err := stream.Send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Proof{Proof: &mimirv1.Proof{Signature: sig}}}); err != nil {
		t.Fatal(err)
	}
	if w, err := stream.Receive(); err != nil || w.GetWelcome() == nil {
		t.Fatalf("welcome = %v, %v", w, err)
	}
	got := make(chan *mimirv1.LinkResponse, 16)
	go func() {
		defer close(got)
		for {
			msg, err := stream.Receive()
			if err != nil {
				return
			}
			got <- msg
		}
	}()
	return stream, got
}

func testPool() *Pool { return New(slog.New(slog.NewTextHandler(io.Discard, nil))) }

func tableMLP(t *testing.T) arch.Architecture {
	t.Helper()
	a, ok := arch.Lookup("table-mlp")
	if !ok {
		t.Fatal("no table-mlp architecture")
	}
	return a
}

// A worker that stops heartbeating is dropped after the idle timeout, and
// a prediction waiting on it fails with ErrLinkLost instead of hanging.
func TestIdleLinkDropped(t *testing.T) {
	p := testPool()
	p.IdleTimeout = 300 * time.Millisecond
	_, got := silentWorker(t, p)
	if !p.Online("w1") {
		t.Fatal("worker not online after joining")
	}
	a := tableMLP(t)
	spec := arch.Spec{InputShape: []int{2}}
	errc := make(chan error, 1)
	go func() {
		_, _, ran, err := p.Predict(context.Background(), "m1", "v1", "", a, spec, func() ([]byte, error) { return []byte("weights"), nil }, []float32{1, 2})
		if !ran {
			err = errors.New("no worker took the prediction")
		}
		errc <- err
	}()
	if msg := <-got; msg.GetPredict() == nil {
		t.Fatalf("worker got %v, want a predict task", msg)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, ErrLinkLost) {
			t.Fatalf("Predict on a silent worker = %v, want ErrLinkLost", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Predict on a silent worker never returned")
	}
	if p.Online("w1") {
		t.Fatal("silent worker still online")
	}
	if p.CanPredict("table-mlp", "") {
		t.Fatal("pool still offers the silent worker")
	}
}

// A prediction whose worker drops off before answering is asked of
// another worker, so the caller gets an answer rather than an error.
func TestPredictMovesOffALostWorker(t *testing.T) {
	p := testPool()
	p.IdleTimeout = time.Minute
	hello := func() *mimirv1.Hello { return &mimirv1.Hello{Architectures: []string{"table-mlp"}, Capacity: 1} }
	lost, lostGot := scriptedWorker(t, p, store.Worker{ID: "w1", Name: "lost"}, hello())
	errc := make(chan error, 1)
	var answer []float32
	go func() {
		res, worker, ran, err := p.Predict(context.Background(), "m1", "v1", "", tableMLP(t), arch.Spec{InputShape: []int{2}}, func() ([]byte, error) { return []byte("weights"), nil }, []float32{1, 2})
		switch {
		case !ran:
			err = errors.New("no worker took the prediction")
		case err == nil && worker != "spare":
			err = fmt.Errorf("answered by %q, want spare", worker)
		}
		answer = res.Output
		errc <- err
	}()
	// The prediction goes to w1, the only worker; then a second joins and
	// w1 goes away without answering.
	if msg := <-lostGot; msg.GetPredict() == nil {
		t.Fatalf("w1 got %v, want a predict task", msg)
	}
	spare, spareGot := scriptedWorker(t, p, store.Worker{ID: "w2", Name: "spare"}, hello())
	lost.CloseRequest()
	lost.CloseResponse()
	var task *mimirv1.PredictTask
	select {
	case msg := <-spareGot:
		if task = msg.GetPredict(); task == nil {
			t.Fatalf("w2 got %v, want the predict task", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the prediction never moved to w2")
	}
	if len(task.GetWeights()) == 0 {
		t.Fatal("w2 was asked without the weights it never had")
	}
	if err := spare.Send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_PredictResult{PredictResult: &mimirv1.PredictResult{TaskId: task.GetTaskId(), Output: &mimirv1.Tensor{Values: []float32{0.25, 0.75}}}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Predict never returned")
	}
	if len(answer) != 2 || answer[1] != 0.75 {
		t.Fatalf("answer = %v, want w2's", answer)
	}
}

// A deleted model is forgotten by the workers that were sent it, and only
// by them: the console tells each once and stops counting it as sent.
func TestForgetTellsWorkersThatHaveIt(t *testing.T) {
	p := testPool()
	p.IdleTimeout = time.Minute
	hello := func() *mimirv1.Hello { return &mimirv1.Hello{Architectures: []string{"table-mlp"}, Capacity: 1} }
	_, gotA := scriptedWorker(t, p, store.Worker{ID: "wa", Name: "a"}, hello())
	_, gotB := scriptedWorker(t, p, store.Worker{ID: "wb", Name: "b"}, hello())
	p.mu.Lock()
	for _, l := range p.links {
		if l.worker.ID == "wa" {
			l.mu.Lock()
			l.sent["m1"] = "v1"
			l.mu.Unlock()
		}
	}
	p.mu.Unlock()

	p.Forget("m1", "")
	select {
	case msg := <-gotA:
		if msg.GetForget().GetModelId() != "m1" {
			t.Fatalf("worker a got %v, want to forget m1", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker a was never told to forget m1")
	}
	p.Forget("m1", "") // already forgotten: nothing more to say
	select {
	case msg := <-gotA:
		t.Fatalf("worker a got %v after forgetting m1", msg)
	case msg := <-gotB:
		t.Fatalf("worker b, never sent m1, got %v", msg)
	case <-time.After(200 * time.Millisecond):
	}
}

// A prediction ends with its caller's context, and the worker is told to
// stop; the link itself stays up while the worker heartbeats.
func TestPredictHonoursContext(t *testing.T) {
	p := testPool()
	p.IdleTimeout = time.Minute
	_, got := silentWorker(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, ran, err := p.Predict(ctx, "m1", "v1", "", tableMLP(t), arch.Spec{InputShape: []int{2}}, func() ([]byte, error) { return []byte("weights"), nil }, []float32{1, 2})
	if !ran || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Predict past its deadline = %v, %v", ran, err)
	}
	if msg := <-got; msg.GetPredict() == nil {
		t.Fatalf("first message %v, want the predict task", msg)
	}
	select {
	case msg := <-got:
		if msg.GetCancel() == nil {
			t.Fatalf("second message %v, want a cancel", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker was never told to cancel")
	}
	if !p.Online("w1") {
		t.Fatal("a timed-out prediction dropped the link")
	}
	if !p.CanPredict("table-mlp", "") {
		t.Fatal("the timed-out prediction kept its slot")
	}
}

// Many callers reserving at once never put more tasks on a link than its
// capacity. Run with -race: it also catches unguarded reads of busy.
func TestReserveRespectsCapacity(t *testing.T) {
	p := testPool()
	caps := map[string]int{"a": 3, "b": 2}
	for id, c := range caps {
		p.links[id] = &link{id: id, worker: store.Worker{ID: id}, hello: &mimirv1.Hello{Architectures: []string{"x"}, Capacity: int32(c)},
			pending: map[string]chan *mimirv1.LinkRequest{}, tasks: map[string]Task{}}
	}
	for range 50 { // repeat: a race needs a lucky interleaving to show
		var wg sync.WaitGroup
		var taken atomic.Int32
		var mu sync.Mutex
		var held []func()
		start := make(chan struct{})
		for range 32 {
			wg.Go(func() {
				<-start
				l, id, _ := p.reserve("x", false, "", Task{Kind: "predict"})
				if l == nil {
					return
				}
				taken.Add(1)
				p.CanPredict("x", "") // reads busy alongside the reservations
				l.mu.Lock()
				over := l.busy > int(l.hello.Capacity)
				l.mu.Unlock()
				if over {
					t.Errorf("link %s over capacity", l.id)
				}
				mu.Lock()
				held = append(held, func() { l.finish(id) })
				mu.Unlock()
			})
		}
		close(start)
		wg.Wait()
		if n := taken.Load(); n != 5 {
			t.Fatalf("%d reservations held at once, want 5 (the total capacity)", n)
		}
		if p.CanPredict("x", "") {
			t.Fatal("a full pool still offers a link")
		}
		for _, f := range held {
			f()
		}
		for id := range caps {
			if b := p.links[id].busy; b != 0 {
				t.Fatalf("link %s busy %d after every task finished", id, b)
			}
		}
	}
}

// Close ends a connected worker's link at once, so a server shutting down
// doesn't wait out its timeout, and refuses links that come after.
func TestCloseEndsLinks(t *testing.T) {
	p := testPool()
	_, got := silentWorker(t, p)
	if !p.Online("w1") {
		t.Fatal("worker not online after joining")
	}
	p.Close()
	select {
	case _, open := <-got:
		if open {
			t.Fatal("worker got a message instead of the link ending")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("link still open after Close")
	}
	if p.Online("w1") {
		t.Fatal("worker still online after Close")
	}
	p.Close() // a second Close is harmless
}

// A worker that only trains is offered training and never predictions,
// and counts towards the training slots.
func TestTrainingOnlyWorker(t *testing.T) {
	p := testPool()
	newLink := func(id string, hello *mimirv1.Hello) {
		p.links[id] = &link{id: id, worker: store.Worker{ID: id}, hello: hello, pending: map[string]chan *mimirv1.LinkRequest{}, tasks: map[string]Task{}}
	}
	newLink("trainer", &mimirv1.Hello{Architectures: []string{"x"}, Trains: true, TrainingOnly: true, Capacity: 3})
	newLink("server", &mimirv1.Hello{Architectures: []string{"x"}, Trains: false, Capacity: 2})

	if p.CanPredict("x", "trainer") {
		t.Fatal("a training-only worker was offered a prediction")
	}
	if !p.CanTrain("x", "trainer") {
		t.Fatal("a training-only worker wasn't offered training")
	}
	if p.CanTrain("x", "server") {
		t.Fatal("a serving-only worker was offered training")
	}
	if !p.CanPredict("x", "server") {
		t.Fatal("a serving-only worker wasn't offered a prediction")
	}
	if l := p.pick("x", false, ""); l == nil || l.id != "server" {
		t.Fatalf("automatic prediction picked %v, want the serving worker", l)
	}
	if l := p.pick("x", true, ""); l == nil || l.id != "trainer" {
		t.Fatalf("automatic training picked %v, want the training worker", l)
	}
	if n := p.TrainSlots(); n != 3 {
		t.Fatalf("TrainSlots = %d, want 3", n)
	}
	if !p.TrainingOnly("trainer") || p.TrainingOnly("server") || p.TrainingOnly("offline") {
		t.Fatal("TrainingOnly is wrong")
	}

	// The console's choice narrows what a worker said: a worker that runs
	// and teaches is told to teach only, then to answer only.
	newLink("both", &mimirv1.Hello{Architectures: []string{"x"}, Trains: true, Capacity: 4})
	if !p.CanPredict("x", "both") || !p.CanTrain("x", "both") || p.TrainSlots() != 7 {
		t.Fatal("a worker that does both isn't offered both")
	}
	p.SetTasks("both", "train")
	if p.CanPredict("x", "both") || !p.CanTrain("x", "both") || !p.TrainingOnly("both") {
		t.Fatal("told to train only, the worker is still offered predictions")
	}
	p.SetTasks("both", "serve")
	if !p.CanPredict("x", "both") || p.CanTrain("x", "both") || p.TrainSlots() != 3 {
		t.Fatal("told to serve only, the worker is still offered training")
	}
	p.SetTasks("both", "")
	if !p.CanPredict("x", "both") || !p.CanTrain("x", "both") {
		t.Fatal("back to its own choice, the worker isn't offered both")
	}
	for _, st := range p.Statuses() {
		if st.Worker.ID == "both" && (!st.Trains || st.TrainingOnly) {
			t.Fatalf("status of both = %+v", st)
		}
	}
}
