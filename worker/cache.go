package worker

import (
	"container/list"
	"sync"
)

// modelCache keeps the models a worker was sent, by id, within a byte
// budget. Each model counts the size of the weights it came with, whether
// the worker still holds them or a Loader does. The least recently used
// models go first; the one added last always stays.
type modelCache struct {
	mu    sync.Mutex
	items map[string]*cached
	lru   list.List // of *cached, most recently used first
	used  int64
	limit int64 // 0 means no limit
}

type cached struct {
	m      *Model
	size   int64
	unload func() // tells a Loader to let go of the model; may be nil
	elem   *list.Element
}

func newModelCache(limit int64) *modelCache {
	return &modelCache{items: map[string]*cached{}, limit: max(limit, 0)}
}

// get returns the model with id and marks it most recently used, or nil.
func (c *modelCache) get(id string) *Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	it := c.items[id]
	if it == nil {
		return nil
	}
	c.lru.MoveToFront(it.elem)
	return it.m
}

// put adds m, replacing any model with its id, then drops the least
// recently used others until the cache is within its limit, unloading
// them. A replaced model is not unloaded: the new one has its id.
func (c *modelCache) put(m *Model, size int64, unload func()) {
	c.mu.Lock()
	if old := c.items[m.ID]; old != nil {
		c.lru.Remove(old.elem)
		c.used -= old.size
	}
	it := &cached{m: m, size: size, unload: unload}
	it.elem = c.lru.PushFront(it)
	c.items[m.ID] = it
	c.used += size
	var gone []*cached
	for c.limit > 0 && c.used > c.limit {
		back := c.lru.Back()
		if back == nil || back == it.elem {
			break
		}
		old := back.Value.(*cached)
		c.lru.Remove(back)
		delete(c.items, old.m.ID)
		c.used -= old.size
		gone = append(gone, old)
	}
	c.mu.Unlock()
	for _, old := range gone {
		if old.unload != nil {
			old.unload()
		}
	}
}

// remove drops m if it is still the model for its id, without unloading
// it: the caller found its handler had already let it go.
func (c *modelCache) remove(m *Model) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it := c.items[m.ID]; it != nil && it.m == m {
		c.lru.Remove(it.elem)
		delete(c.items, m.ID)
		c.used -= it.size
	}
}

// forget drops the model with id, unloading it, when the console says
// it is gone.
func (c *modelCache) forget(id string) {
	c.mu.Lock()
	it := c.items[id]
	if it != nil {
		c.lru.Remove(it.elem)
		delete(c.items, id)
		c.used -= it.size
	}
	c.mu.Unlock()
	if it != nil && it.unload != nil {
		it.unload()
	}
}

// shed drops the least recently used models, unloading them, until at
// most keep bytes are held, keeping the one used last. It returns the
// bytes that went.
func (c *modelCache) shed(keep int64) int64 {
	c.mu.Lock()
	before := c.used
	var gone []*cached
	for c.used > max(keep, 0) && c.lru.Len() > 1 {
		old := c.lru.Back().Value.(*cached)
		c.lru.Remove(old.elem)
		delete(c.items, old.m.ID)
		c.used -= old.size
		gone = append(gone, old)
	}
	freed := before - c.used
	c.mu.Unlock()
	for _, old := range gone {
		if old.unload != nil {
			old.unload()
		}
	}
	return freed
}

// usage returns the bytes and number of models held.
func (c *modelCache) usage() (int64, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used, len(c.items)
}
