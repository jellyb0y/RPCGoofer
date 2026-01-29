package cache

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// cacheEntry represents a cached item with expiration
type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

// MemoryCache is an in-memory LRU cache with TTL support
type MemoryCache struct {
	cache *lru.Cache[string, *cacheEntry]
	ttl   time.Duration
	mu    sync.RWMutex
}

// NewMemoryCache creates a new in-memory cache
func NewMemoryCache(size int, ttl time.Duration) (*MemoryCache, error) {
	cache, err := lru.New[string, *cacheEntry](size)
	if err != nil {
		return nil, err
	}

	mc := &MemoryCache{
		cache: cache,
		ttl:   ttl,
	}

	// Start background cleanup goroutine
	go mc.cleanupLoop()

	return mc, nil
}

// Get retrieves a value from the cache
func (mc *MemoryCache) Get(key string) ([]byte, bool) {
	mc.mu.RLock()
	entry, ok := mc.cache.Get(key)
	mc.mu.RUnlock()

	if !ok {
		return nil, false
	}

	// Check if entry has expired
	if time.Now().After(entry.expiresAt) {
		mc.mu.Lock()
		mc.cache.Remove(key)
		mc.mu.Unlock()
		return nil, false
	}

	return entry.data, true
}

// Set stores a value in the cache
func (mc *MemoryCache) Set(key string, value []byte) {
	entry := &cacheEntry{
		data:      value,
		expiresAt: time.Now().Add(mc.ttl),
	}

	mc.mu.Lock()
	mc.cache.Add(key, entry)
	mc.mu.Unlock()
}

// Close stops the cache cleanup goroutine
func (mc *MemoryCache) Close() {
	// LRU cache doesn't need explicit closing
	// The cleanup goroutine will stop when the cache is garbage collected
}

// cleanupLoop periodically removes expired entries
func (mc *MemoryCache) cleanupLoop() {
	ticker := time.NewTicker(mc.ttl / 2)
	defer ticker.Stop()

	for range ticker.C {
		mc.removeExpired()
	}
}

// removeExpired removes all expired entries from the cache
func (mc *MemoryCache) removeExpired() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	now := time.Now()
	keys := mc.cache.Keys()

	for _, key := range keys {
		entry, ok := mc.cache.Peek(key)
		if ok && now.After(entry.expiresAt) {
			mc.cache.Remove(key)
		}
	}
}

// NoopCache is a cache that does nothing (used when caching is disabled)
type NoopCache struct{}

// NewNoopCache creates a new no-op cache
func NewNoopCache() *NoopCache {
	return &NoopCache{}
}

// Get always returns not found
func (nc *NoopCache) Get(key string) ([]byte, bool) {
	return nil, false
}

// Set does nothing
func (nc *NoopCache) Set(key string, value []byte) {}

// Close does nothing
func (nc *NoopCache) Close() {}
