package balancer

import "rpcgofer/internal/upstream"

// Selector defines the interface for selecting an upstream
type Selector interface {
	// Next returns the next upstream to use, excluding any in the exclude map
	// Returns nil if no suitable upstream is available
	Next(exclude map[string]bool) *upstream.Upstream
}

// UpstreamProvider provides access to upstreams
type UpstreamProvider interface {
	// GetForRequest returns upstreams suitable for handling requests
	// (main if available, otherwise fallback)
	GetForRequest() []*upstream.Upstream

	// GetHealthyMain returns healthy main upstreams
	GetHealthyMain() []*upstream.Upstream

	// GetHealthyFallback returns healthy fallback upstreams
	GetHealthyFallback() []*upstream.Upstream
}
