package proxy

import (
	"fmt"
	"strings"
	"sync"

	"rpcgofer/internal/upstream"
)

// Router manages routing requests to the appropriate upstream pool
type Router struct {
	pools map[string]*upstream.Pool
	mu    sync.RWMutex
}

// NewRouter creates a new Router
func NewRouter() *Router {
	return &Router{
		pools: make(map[string]*upstream.Pool),
	}
}

// AddPool adds a pool to the router
func (r *Router) AddPool(pool *upstream.Pool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pools[pool.Name()] = pool
}

// GetPool returns the pool for the given group name
func (r *Router) GetPool(groupName string) (*upstream.Pool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	pool, ok := r.pools[groupName]
	if !ok {
		return nil, fmt.Errorf("group '%s' not found", groupName)
	}
	return pool, nil
}

// GetPoolFromPath extracts the group name from a URL path and returns the pool
// Path format: /{groupName} or /{groupName}/
func (r *Router) GetPoolFromPath(path string) (*upstream.Pool, error) {
	groupName := extractGroupName(path)
	if groupName == "" {
		return nil, fmt.Errorf("invalid path: group name is required")
	}
	return r.GetPool(groupName)
}

// extractGroupName extracts the group name from a URL path
// Examples:
//   /ethereum -> ethereum
//   /ethereum/ -> ethereum
//   /polygon/some/path -> polygon
func extractGroupName(path string) string {
	// Remove leading slash
	path = strings.TrimPrefix(path, "/")

	// Get the first path segment
	if idx := strings.Index(path, "/"); idx != -1 {
		path = path[:idx]
	}

	return path
}

// GetAllPools returns all registered pools
func (r *Router) GetAllPools() []*upstream.Pool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	pools := make([]*upstream.Pool, 0, len(r.pools))
	for _, pool := range r.pools {
		pools = append(pools, pool)
	}
	return pools
}

// GetGroupNames returns all registered group names
func (r *Router) GetGroupNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.pools))
	for name := range r.pools {
		names = append(names, name)
	}
	return names
}

// HasPool returns true if a pool exists for the given group name
func (r *Router) HasPool(groupName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.pools[groupName]
	return ok
}

// StartAll starts all pools
func (r *Router) StartAll() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, pool := range r.pools {
		pool.Start()
	}
}

// StopAll stops all pools
func (r *Router) StopAll() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, pool := range r.pools {
		pool.Stop()
	}
}
