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
