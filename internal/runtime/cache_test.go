package runtime

import "testing"

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
		e, err := c.get(id, "v1", func() ([]byte, error) {
			loads[id]++
			return make([]byte, size), nil
		}, func(*entry, []byte) {})
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
	if _, err := c.get("small", "v2", func() ([]byte, error) { return make([]byte, 20), nil }, func(*entry, []byte) {}); err != nil {
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
