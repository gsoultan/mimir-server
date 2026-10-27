package cluster

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/training"
)

// groupGrace is how long the members of a group that is stopping have to
// say they stopped.
const groupGrace = 30 * time.Second

// TrainGroup trains t on up to most workers together, one run split
// between them (see training.Group): each learns from its share of the
// batches, and every few steps the pool averages their weights and sends
// the mean back. It takes only workers that train t's architecture in
// groups, have a slot free and aren't short of memory, and runs only when
// at least two do; ran is false otherwise and nothing was sent. workers names them, member 0
// first. If any member fails or its link drops, the others are stopped
// and the run fails with that error: ErrLinkLost, or a member stopping
// for memory (training.ErrLowMemory), puts it back in the queue.
func (p *Pool) TrainGroup(ctx context.Context, jobID string, most int, t training.Task, progress training.Progress) (res training.Result, workers string, ran bool, err error) {
	if tooBigToSend(t) {
		return res, "", false, nil
	}
	members := p.reserveGroup(t.Arch.Name, most, Task{Kind: "train", JobID: jobID})
	if len(members) < 2 {
		for _, m := range members {
			m.l.finish(m.id)
		}
		return res, "", false, nil
	}
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.l.worker.Name
		defer m.l.finish(m.id)
	}
	workers = strings.Join(names, ", ")
	g := &group{members: members, rounds: map[int64]*round{}}
	base := trainTask("", jobID, t)
	base.GroupSize = int32(len(members))
	for i, m := range members {
		task := proto.CloneOf(base)
		task.TaskId, task.GroupRank = m.id, int32(i)
		if err := m.l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Train{Train: task}}); err != nil {
			for j := range i {
				g.stop(j)
			}
			return res, workers, true, ErrLinkLost
		}
	}

	type event struct {
		member int
		msg    *mimirv1.LinkRequest
		err    error
	}
	events := make(chan event)
	listen, quit := context.WithCancel(context.WithoutCancel(ctx))
	defer quit()
	for i, m := range members {
		go func() {
			for {
				msg, err := wait(listen, m.replies, m.l.closed)
				if errors.Is(err, context.Canceled) && listen.Err() != nil {
					return // TrainGroup has returned
				}
				select {
				case events <- event{i, msg, err}:
				case <-listen.Done():
					return
				}
				if err != nil || msg.GetTrainResult() != nil {
					return
				}
			}
		}()
	}

	var (
		failure error
		grace   <-chan time.Time
		done    = ctx.Done()
		left    = len(members)
		ended   = make([]bool, len(members))
	)
	fail := func(err error) {
		if failure == nil {
			failure = err
		}
		if grace == nil {
			grace = time.After(groupGrace)
			for i := range members {
				if !ended[i] {
					g.stop(i)
				}
			}
		}
	}
	for left > 0 {
		var ev event
		select {
		case ev = <-events:
		case <-done:
			done = nil
			fail(cmpCause(ctx))
			continue
		case <-grace:
			return res, workers, true, failure
		}
		m := members[ev.member]
		if ev.err != nil {
			ended[ev.member] = true
			left--
			fail(fmt.Errorf("%w: %s", ErrLinkLost, m.l.worker.Name))
			continue
		}
		switch b := ev.msg.Body.(type) {
		case *mimirv1.LinkRequest_TrainProgress:
			if ev.member == 0 && progress != nil && failure == nil {
				pr := b.TrainProgress
				progress(int(pr.Epoch), int(pr.Step), int(pr.StepsPerEpoch), EpochsFromProto(pr.History))
			}
		case *mimirv1.LinkRequest_GroupShare:
			if failure != nil {
				continue
			}
			if err := g.share(ev.member, b.GroupShare); err != nil {
				fail(fmt.Errorf("%w: %s: %v", ErrWorkerFailed, m.l.worker.Name, err))
			}
		case *mimirv1.LinkRequest_TrainResult:
			ended[ev.member] = true
			left--
			r := b.TrainResult
			switch {
			case r.LowMemory:
				fail(fmt.Errorf("%w on %s", training.ErrLowMemory, m.l.worker.Name))
			case r.Cancelled:
				fail(cmpCause(ctx))
			case r.Error != "":
				fail(fmt.Errorf("%w: %s: %s", ErrWorkerFailed, m.l.worker.Name, r.Error))
			case ev.member == 0:
				res = resultOf(r)
			}
		}
	}
	if failure != nil {
		return training.Result{}, workers, true, failure
	}
	return res, workers, true, nil
}

// member is one worker of a group, with the slot it holds.
type member struct {
	l       *link
	id      string
	replies chan *mimirv1.LinkRequest
}

// reserveGroup takes a slot on up to most workers that train archName in
// groups, the least busy first, one link per worker.
func (p *Pool) reserveGroup(archName string, most int, t Task) []member {
	p.mu.Lock()
	defer p.mu.Unlock()
	type candidate struct {
		l    *link
		busy int
	}
	var cs []candidate
	seen := map[string]bool{}
	for _, l := range p.links {
		if seen[l.worker.ID] || !l.trains() || !slices.Contains(l.hello.GroupArchitectures, archName) || !slices.Contains(l.hello.Architectures, archName) {
			continue
		}
		l.mu.Lock()
		busy, cap := l.busy, int(l.hello.Capacity)
		pressure := l.stats.GetMemoryPressure()
		l.mu.Unlock()
		if busy >= cap || pressure >= pressureHigh {
			continue // full, or short of memory: it takes no training runs
		}
		seen[l.worker.ID] = true
		cs = append(cs, candidate{l, busy})
	}
	slices.SortStableFunc(cs, func(a, b candidate) int {
		if a.busy != b.busy {
			return a.busy - b.busy
		}
		return strings.Compare(a.l.worker.Name, b.l.worker.Name)
	})
	var out []member
	for _, c := range cs[:min(most, len(cs))] {
		c.l.mu.Lock()
		id, ch := c.l.open(t)
		c.l.mu.Unlock()
		out = append(out, member{c.l, id, ch})
	}
	return out
}

// group averages the weights the members of one run share.
type group struct {
	members []member
	rounds  map[int64]*round
}

// round is one average in the making.
type round struct {
	sum    []float64
	weight float64
	from   []bool // which members have sent theirs
	got    int
}

// share adds a member's values to their round, and sends every member the
// mean once all have.
func (g *group) share(i int, s *mimirv1.GroupShare) error {
	r := g.rounds[s.Round]
	if r == nil {
		r = &round{sum: make([]float64, len(s.Values)), from: make([]bool, len(g.members))}
		g.rounds[s.Round] = r
	}
	switch {
	case r.from[i]:
		return fmt.Errorf("sent round %d twice", s.Round)
	case len(s.Values) != len(r.sum):
		return fmt.Errorf("sent %d weights for round %d, another member %d", len(s.Values), s.Round, len(r.sum))
	case s.Weight < 0:
		return fmt.Errorf("weighed round %d by %v", s.Round, s.Weight)
	}
	r.from[i] = true
	r.got++
	r.weight += s.Weight
	for k, v := range s.Values {
		r.sum[k] += float64(v) * s.Weight
	}
	if r.got < len(g.members) {
		return nil
	}
	delete(g.rounds, s.Round)
	if r.weight <= 0 {
		return fmt.Errorf("round %d weighs nothing", s.Round)
	}
	mean := make([]float32, len(r.sum))
	for k, v := range r.sum {
		mean[k] = float32(v / r.weight)
	}
	// The members wait for the mean, so it goes to all at once; a member
	// whose link has gone shows as lost on its own.
	var wg sync.WaitGroup
	for _, m := range g.members {
		wg.Go(func() {
			m.l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_GroupMean{GroupMean: &mimirv1.GroupMean{TaskId: m.id, Round: s.Round, Values: mean}}})
		})
	}
	wg.Wait()
	return nil
}

// stop asks member i to stop training.
func (g *group) stop(i int) {
	m := g.members[i]
	m.l.send(&mimirv1.LinkResponse{Body: &mimirv1.LinkResponse_Cancel{Cancel: &mimirv1.CancelTask{TaskId: m.id}}})
}
