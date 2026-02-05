package balancer

import "rpcgofer/internal/upstream"

// Selector is the interface for selecting an upstream; same as upstream.Selector for compatibility
type Selector = upstream.Selector

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
