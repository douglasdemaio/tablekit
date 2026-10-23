package auth

import (
	"sync"
	"time"
)

type limiterEntry struct {
	count int
	reset time.Time
}

// Limiter is a process-local fixed-window limiter. It is intentionally small;
// a multi-instance deployment must move this to a shared store (or use a
// gateway-level limiter) in a later milestone.
type Limiter struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry
	now     func() time.Time
}

func NewLimiter() *Limiter {
	return &Limiter{entries: make(map[string]*limiterEntry), now: time.Now}
}

func (l *Limiter) Allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.entries) > 10_000 {
		for k, e := range l.entries {
			if now.After(e.reset) {
				delete(l.entries, k)
			}
		}
	}
	e, ok := l.entries[key]
	if !ok || now.After(e.reset) {
		l.entries[key] = &limiterEntry{count: 1, reset: now.Add(window)}
		return true
	}
	if e.count >= limit {
		return false
	}
	e.count++
	return true
}
