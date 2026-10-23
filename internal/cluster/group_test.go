package cluster

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
)

// groupWorker joins p as a worker that trains table-mlp in groups.
func groupWorker(t *testing.T, p *Pool, name string) (send func(*mimirv1.LinkRequest), got <-chan *mimirv1.LinkResponse, hangUp func()) {
	t.Helper()
	stream, got := scriptedWorker(t, p, store.Worker{ID: name, Name: name}, &mimirv1.Hello{Architectures: []string{"table-mlp"}, GroupArchitectures: []string{"table-mlp"}, Trains: true, Capacity: 1})
	send = func(m *mimirv1.LinkRequest) {
		t.Helper()
		if err := stream.Send(m); err != nil {
			t.Error(err)
		}
	}
	return send, got, func() { stream.CloseRequest(); stream.CloseResponse() }
}

func next(t *testing.T, got <-chan *mimirv1.LinkResponse) *mimirv1.LinkResponse {
	t.Helper()
	select {
	case m := <-got:
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("the pool sent nothing")
		return nil
	}
}

type groupRun struct {
	res     training.Result
	workers string
	ran     bool
	err     error
}

func trainGroup(p *Pool, t training.Task, most int) <-chan groupRun {
	out := make(chan groupRun, 1)
	go func() {
		var r groupRun
		r.res, r.workers, r.ran, r.err = p.TrainGroup(context.Background(), "job", most, t, nil)
		out <- r
	}()
	return out
}

// TestTrainGroup splits a run between two workers, averages what they
// share, keeps member 0's weights, and stops the run when a member goes.
func TestTrainGroup(t *testing.T) {
	p := testPool()
	task := training.Task{Arch: tableMLP(t), Settings: store.Settings{Epochs: 1, BatchSize: 8}}

	// One worker that trains in groups isn't a group.
	sendA, gotA, _ := groupWorker(t, p, "alpha")
	if r := <-trainGroup(p, task, 4); r.ran {
		t.Fatalf("one worker ran a group: %+v", r)
	}
	sendB, gotB, hangUpB := groupWorker(t, p, "beta")

	run := trainGroup(p, task, 4)
	ta, tb := next(t, gotA).GetTrain(), next(t, gotB).GetTrain()
	if ta.GetGroupRank() != 0 || tb.GetGroupRank() != 1 || ta.GetGroupSize() != 2 || tb.GetGroupSize() != 2 {
		t.Fatalf("tasks %v and %v", ta, tb)
	}
	// Member 0 starts the others from its weights: the mean weighs only its.
	sendA(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_GroupShare{GroupShare: &mimirv1.GroupShare{TaskId: ta.TaskId, Round: 0, Values: []float32{1, 2}, Weight: 1}}})
	sendB(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_GroupShare{GroupShare: &mimirv1.GroupShare{TaskId: tb.TaskId, Round: 0, Values: []float32{5, 6}, Weight: 0}}})
	for _, got := range []<-chan *mimirv1.LinkResponse{gotA, gotB} {
		if m := next(t, got).GetGroupMean(); m.GetRound() != 0 || !slices.Equal(m.GetValues(), []float32{1, 2}) {
			t.Fatalf("round 0 mean = %v", m)
		}
	}
	sendB(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_GroupShare{GroupShare: &mimirv1.GroupShare{TaskId: tb.TaskId, Round: 1, Values: []float32{4, 0}, Weight: 1}}})
	sendA(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_GroupShare{GroupShare: &mimirv1.GroupShare{TaskId: ta.TaskId, Round: 1, Values: []float32{2, 2}, Weight: 1}}})
	for _, got := range []<-chan *mimirv1.LinkResponse{gotA, gotB} {
		if m := next(t, got).GetGroupMean(); m.GetRound() != 1 || !slices.Equal(m.GetValues(), []float32{3, 1}) {
			t.Fatalf("round 1 mean = %v", m)
		}
	}
	sendB(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_TrainResult{TrainResult: &mimirv1.TrainResult{TaskId: tb.TaskId}}})
	sendA(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_TrainResult{TrainResult: &mimirv1.TrainResult{TaskId: ta.TaskId, Weights: []byte("model")}}})
	r := <-run
	if r.err != nil || !r.ran || string(r.res.Weights) != "model" || r.workers != "alpha, beta" {
		t.Fatalf("TrainGroup = %+v", r)
	}

	// A member that goes stops the rest, and the run goes back in the queue.
	run = trainGroup(p, task, 4)
	ta, _ = next(t, gotA).GetTrain(), next(t, gotB).GetTrain()
	hangUpB()
	if m := next(t, gotA); m.GetCancel().GetTaskId() != ta.TaskId {
		t.Fatalf("alpha got %v, want its task cancelled", m)
	}
	sendA(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_TrainResult{TrainResult: &mimirv1.TrainResult{TaskId: ta.TaskId, Cancelled: true}}})
	if r := <-run; !errors.Is(r.err, training.ErrWorkerLost) {
		t.Fatalf("TrainGroup after beta left = %+v", r)
	}
}
