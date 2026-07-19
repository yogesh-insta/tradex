package dashboard

import (
	"sync"
	"time"
)

// ttlCache is a tiny process-memory cache keyed by string.
type ttlCache struct {
	mu    sync.Mutex
	items map[string]cacheItem
}

type cacheItem struct {
	expires time.Time
	value   any
}

func newTTLCache() *ttlCache {
	return &ttlCache{items: map[string]cacheItem{}}
}

func (c *ttlCache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || time.Now().After(it.expires) {
		if ok {
			delete(c.items, key)
		}
		return nil, false
	}
	return it.value, true
}

func (c *ttlCache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheItem{expires: time.Now().Add(ttl), value: value}
}

// Invalidate clears one key (tests).
func (c *ttlCache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
