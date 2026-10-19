package auth

import (
	"sync"
	"time"
)

// Limiter slows down password and code guessing. Each key (an account or
// an address) gets a budget of failures per window; once spent, attempts
// are refused until the window passes. Success clears the key.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*bucket
	now    func() time.Time
}

type bucket struct {
	n     int
	reset time.Time
}

// NewLimiter allows max failures per window for each key.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, hits: map[string]*bucket{}, now: time.Now}
}

// Wait returns how long key is locked out, or zero when it may try.
func (l *Limiter) Wait(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for _, k := range keys {
		b, ok := l.hits[k]
		if !ok {
			continue
		}
		if !now.Before(b.reset) {
			delete(l.hits, k)
			continue
		}
		if b.n >= l.max {
			wait = max(wait, b.reset.Sub(now))
		}
	}
	return wait
}

// Fail records a failed attempt for each key.
func (l *Limiter) Fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.hits) > 10_000 { // keep memory bounded under a flood
		for k, b := range l.hits {
			if !now.Before(b.reset) {
				delete(l.hits, k)
			}
		}
	}
	for _, k := range keys {
		b, ok := l.hits[k]
		if !ok || !now.Before(b.reset) {
			b = &bucket{reset: now.Add(l.window)}
			l.hits[k] = b
		}
		b.n++
	}
}

// Clear forgets failures for each key, after a success.
func (l *Limiter) Clear(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.hits, k)
	}
}

// Backoff slows down guessing without locking anyone out. Each key gets a
// few free failures; after that every failure doubles how long the key
// must wait before its next try, up to a cap. Keyed on an account and the
// address together, it keeps one address's guesses slow while the
// account's owner, elsewhere, signs in as usual. A key is forgotten after
// a success, or once it has been quiet for a day.
type Backoff struct {
	mu     sync.Mutex
	free   int
	base   time.Duration
	cap    time.Duration
	forget time.Duration
	keys   map[string]*streak
	now    func() time.Time
}

type streak struct {
	n     int
	until time.Time // no tries before this
	last  time.Time // the latest failure
}

// NewBackoff allows free failures per key, then makes the key wait base,
// twice base, four times base and so on, never more than max.
func NewBackoff(free int, base, max time.Duration) *Backoff {
	return &Backoff{free: free, base: base, cap: max, forget: 24 * time.Hour, keys: map[string]*streak{}, now: time.Now}
}

// Wait returns how long key must wait before its next try, or zero.
func (b *Backoff) Wait(key string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	s, ok := b.keys[key]
	if !ok {
		return 0
	}
	if now.Sub(s.last) >= b.forget {
		delete(b.keys, key)
		return 0
	}
	return max(s.until.Sub(now), 0)
}

// Fail records a failed try for key and returns how long it must now wait.
func (b *Backoff) Fail(key string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if len(b.keys) > 10_000 { // keep memory bounded under a flood
		for k, s := range b.keys {
			if now.Sub(s.last) >= b.forget || (s.n <= b.free && now.Sub(s.last) >= b.cap) {
				delete(b.keys, k)
			}
		}
	}
	s, ok := b.keys[key]
	if !ok || now.Sub(s.last) >= b.forget {
		s = &streak{}
		b.keys[key] = s
	}
	s.n++
	s.last = now
	if over := s.n - b.free; over > 0 {
		wait := b.cap
		if over < 63 && b.base <= b.cap>>(over-1) {
			wait = b.base << (over - 1)
		}
		s.until = now.Add(wait)
	}
	return max(s.until.Sub(now), 0)
}

// Clear forgets key, after a success.
func (b *Backoff) Clear(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.keys, key)
}
