package auth

import (
	"sync"
	"time"
)

type window struct {
	count int
	until time.Time
}

// Limiter is a bounded, fixed-window limiter. Callers use the physical peer IP or
// a hashed session token, never attacker-controlled X-Forwarded-For.
type Limiter struct {
	mu      sync.Mutex
	entries map[string]window
	limit   int
	period  time.Duration
}

func NewLimiter(limit int, period time.Duration) *Limiter {
	return &Limiter{entries: map[string]window{}, limit: limit, period: period}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	w, exists := l.entries[key]
	if !exists || !now.Before(w.until) {
		for k, old := range l.entries {
			if !now.Before(old.until) {
				delete(l.entries, k)
			}
		}
		if len(l.entries) >= 4096 {
			return false
		}
		w = window{until: now.Add(l.period)}
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	l.entries[key] = w
	return true
}
