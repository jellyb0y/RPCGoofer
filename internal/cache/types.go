package cache

// Cache defines the interface for RPC response caching
// This interface allows for different implementations (in-memory, Redis, etc.)
type Cache interface {
	// Get retrieves a cached response by key
	// Returns the cached data and true if found, nil and false otherwise
	Get(key string) ([]byte, bool)

	// Set stores a response in the cache with the given key
	Set(key string, value []byte)

	// Close releases any resources held by the cache
	Close()
}

// CacheResult represents the result of a cache lookup
type CacheResult struct {
	Data []byte
	Hit  bool
}
