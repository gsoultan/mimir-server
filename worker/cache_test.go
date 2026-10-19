package worker

import "testing"

// The worker drops the least recently used models, unloading them from
// their handler, to stay within its byte budget, and keeps the newest.
func TestModelCacheEvictsByBytes(t *testing.T) {
	c := newModelCache(100)
	unloaded := map[string]int{}
	put := func(id string, size int64) *Model {
		m := &Model{ID: id}
		c.put(m, size, func() { unloaded[id]++ })
		return m
	}
	put("a", 40)
	put("b", 40)
	if c.get("a") == nil { // a is now the most recently used
		t.Fatal("a missing")
	}
	put("c", 40)
	if c.get("b") != nil || unloaded["b"] != 1 {
		t.Fatalf("b should have been dropped and unloaded once; unloaded %v", unloaded)
	}
	if c.get("a") == nil || c.get("c") == nil {
		t.Fatal("a and c should have stayed")
	}
	if b, n := c.usage(); b != 80 || n != 2 {
		t.Fatalf("usage %d bytes in %d models, want 80 in 2", b, n)
	}

	// Sending a model again (a new version) replaces it without unloading.
	put("a", 50)
	if unloaded["a"] != 0 {
		t.Fatal("replacing a model unloaded the new one")
	}
	if b, n := c.usage(); b != 90 || n != 2 {
		t.Fatalf("usage %d bytes in %d models, want 90 in 2", b, n)
	}

	// One model over the whole budget stays, alone.
	put("big", 500)
	if c.get("big") == nil || c.get("a") != nil || c.get("c") != nil {
		t.Fatal("big should be the only model left")
	}

	// remove forgets only the model it was given.
	stale := &Model{ID: "big"}
	c.remove(stale)
	if c.get("big") == nil {
		t.Fatal("removing a stale model dropped the current one")
	}
	c.remove(c.get("big"))
	if b, n := c.usage(); b != 0 || n != 0 {
		t.Fatalf("usage %d bytes in %d models after removing all", b, n)
	}
}
