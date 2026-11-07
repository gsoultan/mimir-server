package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/memguard"
)

// A model retrained in place keeps its id, so the cache must load it again
// when the weights' version changes rather than serve the old module.
func TestCacheReloadsNewVersion(t *testing.T) {
	c := NewCache(0)
	a := c.entry("m", "v1")
	if c.entry("m", "v1") != a {
		t.Fatal("same version: got a new entry")
	}
	b := c.entry("m", "v2")
	if b == a {
		t.Fatal("new version: kept the old entry")
	}
	// A failed load of the old version must not drop the new one.
	c.forget("m", a)
	if c.entry("m", "v2") != b {
		t.Fatal("forgetting the old version dropped the new one")
	}
	c.forget("m", b)
	if c.entry("m", "v2") == b {
		t.Fatal("forget kept the entry")
	}
}

// The cache keeps within its byte limit by dropping the least recently
// used model, loads a dropped model again on its next use, and always
// keeps the model it loaded last.
func TestCacheEvictsByBytes(t *testing.T) {
	c := NewCache(100)
	loads := map[string]int{}
	get := func(id string, size int) *entry {
		t.Helper()
		e, err := c.get(id, "v1", InMemory(func() ([]byte, error) {
			loads[id]++
			return make([]byte, size), nil
		}), func(*entry, []byte) {})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	used := func(wantBytes int64, wantModels int) {
		t.Helper()
		if b, n := c.Used(); b != wantBytes || n != wantModels {
			t.Fatalf("Used() = %d bytes in %d models, want %d in %d", b, n, wantBytes, wantModels)
		}
	}

	get("a", 40)
	get("b", 40)
	used(80, 2)
	get("a", 40) // a is now the most recently used
	get("c", 40) // over the limit: b goes, not a
	used(80, 2)
	if loads["a"] != 1 || loads["b"] != 1 || loads["c"] != 1 {
		t.Fatalf("loads %v, want one each", loads)
	}
	get("b", 40) // dropped, so loaded again; a goes now
	if loads["b"] != 2 {
		t.Fatalf("b loaded %d times, want 2 after it was dropped", loads["b"])
	}
	get("c", 40)
	if loads["c"] != 1 {
		t.Fatalf("c loaded %d times, want 1: it should have stayed", loads["c"])
	}
	used(80, 2)

	// One model over the whole limit still stays, alone.
	get("big", 150)
	used(150, 1)
	get("big", 150)
	if loads["big"] != 1 {
		t.Fatalf("big loaded %d times, want 1", loads["big"])
	}

	// A new version replaces the old one's bytes rather than adding to them.
	c.SetLimit(0)
	get("small", 10)
	if _, err := c.get("small", "v2", InMemory(func() ([]byte, error) { return make([]byte, 20), nil }), func(*entry, []byte) {}); err != nil {
		t.Fatal(err)
	}
	used(170, 2)
	c.Forget("small")
	used(150, 1)

	// Lowering the limit drops what no longer fits, but not the last one.
	get("x", 10)
	c.SetLimit(50)
	used(10, 1)
}

// While the gate says no, no model is loaded, the models loaded keep
// answering, and the next request after it says yes loads.
func TestCacheGate(t *testing.T) {
	c := NewCache(0)
	load := InMemory(func() ([]byte, error) { return make([]byte, 10), nil })
	build := func(*entry, []byte) {}
	if _, err := c.get("loaded", "v1", load, build); err != nil {
		t.Fatal(err)
	}
	low := errors.New("low on memory")
	c.SetGate(func() error { return low })
	if _, err := c.get("loaded", "v1", load, build); err != nil {
		t.Fatalf("a loaded model behind a closed gate: %v", err)
	}
	if _, err := c.get("new", "v1", load, build); !errors.Is(err, low) {
		t.Fatalf("a new model behind a closed gate: %v", err)
	}
	c.SetGate(func() error { return nil })
	if _, err := c.get("new", "v1", load, build); err != nil {
		t.Fatalf("a new model once the gate opens: %v", err)
	}
}

// A load that panics fails its request and leaves the cache to try again.
func TestCacheLoadPanics(t *testing.T) {
	c := NewCache(0)
	load := InMemory(func() ([]byte, error) { return make([]byte, 10), nil })
	if _, err := c.get("m", "v1", load, func(*entry, []byte) { panic("bad file") }); err == nil {
		t.Fatal("a panicking load succeeded")
	}
	if _, err := c.get("m", "v1", load, func(*entry, []byte) {}); err != nil {
		t.Fatalf("the next load: %v", err)
	}
}

// Shed lets go of the least recently used models down to what it keeps,
// but never the last one used.
func TestCacheShed(t *testing.T) {
	c := NewCache(0)
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := c.get(id, "v1", InMemory(func() ([]byte, error) { return make([]byte, 10), nil }), func(*entry, []byte) {}); err != nil {
			t.Fatal(err)
		}
	}
	if freed := c.Shed(20); freed != 20 {
		t.Fatalf("Shed(20) freed %d, want 20", freed)
	}
	if b, n := c.Used(); b != 20 || n != 2 || c.loaded["d"] == nil || c.loaded["c"] == nil {
		t.Fatalf("after Shed(20): %d bytes in %d models; want c and d", b, n)
	}
	c.Shed(0)
	if b, n := c.Used(); b != 10 || n != 1 || c.loaded["d"] == nil {
		t.Fatalf("after Shed(0): %d bytes in %d models; want d alone", b, n)
	}
}

// While a new version of a model loads, the version already loaded keeps
// answering; the switch happens once the new one is in.
func TestCacheKeepsOldVersionWhileNewLoads(t *testing.T) {
	c := NewCache(0)
	load := func(size int) Loader {
		return InMemory(func() ([]byte, error) { return make([]byte, size), nil })
	}
	v1, err := c.get("m", "v1", load(10), func(*entry, []byte) {})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	var v2 *entry
	wg.Go(func() {
		v2, _ = c.get("m", "v2", InMemory(func() ([]byte, error) {
			close(started)
			<-release
			return make([]byte, 20), nil
		}), func(*entry, []byte) {})
	})
	<-started
	// v1 still answers while v2 loads, and nobody waits for v2.
	if e, err := c.get("m", "v1", load(10), func(*entry, []byte) {}); err != nil || e != v1 {
		t.Fatalf("while v2 loads, v1 = %v, %v", e, err)
	}
	if b, n := c.Used(); b != 10 || n != 1 {
		t.Fatalf("while v2 loads, Used() = %d bytes in %d models, want 10 in 1", b, n)
	}
	close(release)
	wg.Wait()
	if e, err := c.get("m", "v2", load(20), func(*entry, []byte) {}); err != nil || e != v2 {
		t.Fatalf("after loading, v2 = %v, %v", e, err)
	}
	if b, n := c.Used(); b != 20 || n != 1 {
		t.Fatalf("after loading, Used() = %d bytes in %d models, want 20 in 1", b, n)
	}
	if c.loaded["m"] != v2 || c.pending["m"] != nil {
		t.Fatal("v2 isn't the loaded entry, or something is still pending")
	}
}

// Critical memory cuts the compute budget to a quarter, but never under
// the floor nor over the normal budget; other levels leave it be.
func TestComputeBudgetAt(t *testing.T) {
	for _, c := range []struct {
		l            memguard.Level
		normal, want int64
	}{
		{memguard.OK, 1 << 30, 1 << 30},
		{memguard.High, 1 << 30, 1 << 30},
		{memguard.Critical, 1 << 30, 1 << 28},
		{memguard.Critical, 128 << 20, 64 << 20},
		{memguard.Critical, 32 << 20, 32 << 20},
	} {
		if got := ComputeBudgetAt(c.l, c.normal); got != c.want {
			t.Errorf("ComputeBudgetAt(%v, %d) = %d, want %d", c.l, c.normal, got, c.want)
		}
	}
}

// forget drops e if it is still the entry, loaded or loading, for id.
func (c *Cache) forget(id string, e *entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgetLocked(id, e)
}

// closing counts how often weights were closed.
type closing struct {
	bytesWeights
	closed *int
}

func (c closing) Close() error { *c.closed++; return nil }

// A model dropped from the cache closes its weights once nothing runs it:
// not while a request holds it, and not while a batch of it is running,
// even one whose asker has gone.
func TestCacheClosesDroppedWeightsAfterLastUse(t *testing.T) {
	c := NewCache(0)
	closed := 0
	load := func() (Weights, error) { return closing{make(bytesWeights, 10), &closed}, nil }
	e, err := c.get("m", "v1", load, func(*entry, []byte) {})
	if err != nil {
		t.Fatal(err)
	}
	c.Forget("m")
	if closed != 0 {
		t.Fatal("closed while a request held the model")
	}
	c.release(e)
	if closed != 1 {
		t.Fatalf("closed %d times after the last release, want 1", closed)
	}
	c.release(e) // a second drop of the same entry must not close it again
	c.Forget("m")
	if closed != 1 {
		t.Fatalf("closed %d times, want 1", closed)
	}

	// A batch runner holds the model for the batch it runs.
	closed = 0
	running := make(chan struct{})
	finish := make(chan struct{})
	e, err = c.get("b", "v1", load, func(e *entry, _ []byte) {
		run := func(context.Context, []float32, int) ([][]float32, error) {
			close(running)
			<-finish
			return [][]float32{{1}}, nil
		}
		e.batch = newBatcher(run, "test", 0, func() Batching { return DefaultBatching }, func() Observer { return nil }, c.holder(e))
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.batch.ask(ctx, []float32{1}); done <- err }()
	<-running
	cancel() // the asker gives up while its question runs
	if err := <-done; err == nil {
		t.Fatal("asker did not see its cancel")
	}
	c.release(e)
	c.Forget("b")
	if closed != 0 {
		t.Fatal("closed while a batch was running")
	}
	close(finish)
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		n := closed
		c.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed %d times after the batch finished, want 1", n)
		}
		time.Sleep(time.Millisecond)
	}
}
