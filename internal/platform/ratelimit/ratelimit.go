// Package ratelimit is an in-memory fixed-window limiter keyed by principal or IP.
// A key's window opens on its first event and lasts one minute, so a caller can
// spend one full limit at the end of one window and another at the start of the
// next. Sizing assumes that; it is not a sliding window.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	window time.Time
	count  int
}

// Limiter limits events per key per minute.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   int
	last    time.Time
}

// New creates a limiter allowing perMinute events per key.
func New(perMinute int) *Limiter {
	return &Limiter{buckets: map[string]*bucket{}, limit: perMinute, last: time.Now()}
}

// Allow records an event and reports whether it is within the limit. The
// second value is the number of seconds until the window resets.
func (l *Limiter) Allow(key string) (bool, int) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.last) > 5*time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.window) > time.Minute {
				delete(l.buckets, k)
			}
		}
		l.last = now
	}
	b, ok := l.buckets[key]
	if !ok || now.Sub(b.window) >= time.Minute {
		l.buckets[key] = &bucket{window: now, count: 1}
		return true, 0
	}
	b.count++
	if b.count > l.limit {
		retry := int(time.Minute-now.Sub(b.window)) / int(time.Second)
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	return true, 0
}
