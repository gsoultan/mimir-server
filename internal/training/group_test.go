package training

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// localGroup is a Group whose members are goroutines of one process.
type localGroup struct {
	size int
	mu   sync.Mutex
	all  []*meanRound // every round so far
}

type meanRound struct {
	sum    []float64
	weight float64
	got    int
	done   chan struct{}
	out    []float32
}

type member struct {
	g    *localGroup
	rank int
	n    int // rounds this member has been in
}

func newLocalGroup(size int) []Group {
	g := &localGroup{size: size}
	out := make([]Group, size)
	for i := range out {
		out[i] = &member{g: g, rank: i}
	}
	return out
}

func (m *member) Rank() int { return m.rank }
func (m *member) Size() int { return m.g.size }

func (m *member) Mean(ctx context.Context, values []float32, weight float64) ([]float32, error) {
	g := m.g
	g.mu.Lock()
	for len(g.all) <= m.n {
		g.all = append(g.all, &meanRound{done: make(chan struct{})})
	}
	r := g.all[m.n]
	m.n++
	if r.sum == nil {
		r.sum = make([]float64, len(values))
	}
	for i, v := range values {
		r.sum[i] += float64(v) * weight
	}
	r.weight += weight
	r.got++
	if r.got == g.size {
		r.out = make([]float32, len(r.sum))
		for i, v := range r.sum {
			r.out[i] = float32(v / r.weight)
		}
		close(r.done)
	}
	g.mu.Unlock()
	select {
	case <-r.done:
		return slices.Clone(r.out), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestGroupFit trains one table run on three members and checks they
// end on the same model, which learns as well as one worker's would.
func TestGroupFit(t *testing.T) {
	csv := largeTable(6000)
	a, _ := arch.Lookup("table-mlp")
	s := TableDefaults
	s.Epochs = 4
	task := Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: csv}, Settings: s}

	alone, err := Fit(context.Background(), task, nil)
	if err != nil {
		t.Fatal(err)
	}

	const size = 3
	groups := newLocalGroup(size)
	results := make([]Result, size)
	errs := make([]error, size)
	steps := make([]int, size)
	var wg sync.WaitGroup
	for i := range size {
		wg.Go(func() {
			tk := task
			tk.Group = groups[i]
			results[i], errs[i] = Fit(context.Background(), tk, func(_, _, n int, _ []store.Epoch) { steps[i] = n })
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("member %d: %v", i, err)
		}
	}
	if steps[1] != 0 {
		t.Errorf("member 1 reported progress")
	}
	if total := (int(float64(6000)*(1-s.HoldOut)) + s.BatchSize - 1) / s.BatchSize; steps[0] != (total+size-1)/size {
		t.Errorf("member 0 took %d steps an epoch; the epoch has %d batches over %d members", steps[0], total, size)
	}
	w0, w1 := weightsOf(t, results[0].Weights), weightsOf(t, results[2].Weights)
	if !slices.Equal(w0, w1) {
		t.Errorf("members 0 and 2 ended on different weights")
	}
	got, want := results[0].History[s.Epochs-1].Accuracy, alone.History[s.Epochs-1].Accuracy
	t.Logf("accuracy: one worker %.3f, %d together %.3f; steps per epoch %d", want, size, got, steps[0])
	if got < want-0.05 {
		t.Errorf("together got %.3f right, alone %.3f", got, want)
	}
}

// weightsOf reads every weight of a saved table model.
func weightsOf(t *testing.T, b []byte) []float32 {
	t.Helper()
	if len(b) == 0 {
		t.Fatal("no weights")
	}
	f, err := mim.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []float32
	for _, n := range f.Names() {
		tt, err := f.Tensor(n)
		if err != nil {
			t.Fatal(err)
		}
		v, err := tt.Cast(tensor.Float32).Data[float32](context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v...)
	}
	return out
}

// TestShard checks the members of a group cover every batch of an epoch
// once between them, each taking the same number of steps, also when
// there are more members than batches.
func TestShard(t *testing.T) {
	for _, c := range []struct{ rows, batch, size int }{{70, 10, 3}, {60, 10, 3}, {20, 10, 5}} {
		train, _, err := split(c.rows, 0, 1, c.batch, &examples{shape: []int{1}, input: func(dst []float32, i int) []float32 { return append(dst, float32(i)) }, num: make([]float32, c.rows)})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[float32]int{}
		for rank := range c.size {
			s := shardOf(train, rank, c.size)
			s.SetEpoch(2)
			n := 0
			for b, err := range s.All(context.Background()) {
				if err != nil {
					t.Fatal(err)
				}
				v, _ := b.X.Data[float32](context.Background())
				for _, x := range v {
					seen[x]++
				}
				n++
			}
			if want := (train.Batches() + c.size - 1) / c.size; n != want || s.Batches() != want {
				t.Errorf("%+v: member %d took %d steps, Batches %d, want %d", c, rank, n, s.Batches(), want)
			}
		}
		for i := range c.rows {
			if seen[float32(i)] == 0 {
				t.Errorf("%+v: example %d never seen", c, i)
			}
		}
	}
}
