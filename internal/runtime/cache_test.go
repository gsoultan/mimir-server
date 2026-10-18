package runtime

import "testing"

// A model retrained in place keeps its id, so the cache must load it again
// when the weights' version changes rather than serve the old module.
func TestCacheReloadsNewVersion(t *testing.T) {
	c := NewCache()
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
