package httpapi

import (
	"sync"
	"time"
)

// ttlCache is a small fixed-size cache with per-entry expiry. It evicts the
// entry with the earliest expiry when the size limit is reached.
type ttlCache[T any] struct {
	mu      sync.Mutex
	entries map[string]cacheEntry[T]
	ttl     time.Duration
	max     int
	now     func() time.Time
}

type cacheEntry[T any] struct {
	value   T
	expires time.Time
}

func newTTLCache[T any](ttl time.Duration, limit int) *ttlCache[T] {
	if limit <= 0 {
		limit = 1
	}
	return &ttlCache[T]{
		entries: make(map[string]cacheEntry[T]),
		ttl:     ttl,
		max:     limit,
		now:     time.Now,
	}
}

// Get returns a cached value if it exists and has not expired.
func (c *ttlCache[T]) Get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		var zero T
		return zero, false
	}
	if c.now().After(entry.expires) {
		delete(c.entries, key)
		var zero T
		return zero, false
	}
	return entry.value, true
}

// Set stores a value until the cache TTL elapses.
func (c *ttlCache[T]) Set(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		var (
			oldestKey string
			oldest    time.Time
			found     bool
		)
		for k, entry := range c.entries {
			if !found || entry.expires.Before(oldest) {
				oldestKey, oldest, found = k, entry.expires, true
			}
		}
		if found {
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = cacheEntry[T]{value: value, expires: c.now().Add(c.ttl)}
}

// Clear removes every cached entry.
func (c *ttlCache[T]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry[T])
}
