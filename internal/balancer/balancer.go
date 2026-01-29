package balancer

import (
	"sync"

	"rpcgofer/internal/upstream"
)

// WeightedRoundRobin implements weighted round-robin load balancing
type WeightedRoundRobin struct {
	provider      UpstreamProvider
	mu            sync.Mutex
	currentIndex  int
	currentWeight int
}

// NewWeightedRoundRobin creates a new WeightedRoundRobin balancer
func NewWeightedRoundRobin(provider UpstreamProvider) *WeightedRoundRobin {
	return &WeightedRoundRobin{
		provider:      provider,
		currentIndex:  -1,
		currentWeight: 0,
	}
}

// Next returns the next upstream using weighted round-robin algorithm
// It filters healthy upstreams and prefers main over fallback
// Excludes upstreams in the exclude map
func (wrr *WeightedRoundRobin) Next(exclude map[string]bool) *upstream.Upstream {
	wrr.mu.Lock()
	defer wrr.mu.Unlock()

	// Get available upstreams (main first, then fallback if no main)
	upstreams := wrr.getAvailable(exclude)
	if len(upstreams) == 0 {
		return nil
	}

	// If only one upstream, return it directly
	if len(upstreams) == 1 {
		return upstreams[0]
	}

	// Calculate GCD and max weight for the current set
	gcd := wrr.gcdWeights(upstreams)
	maxWeight := wrr.maxWeight(upstreams)

	// Weighted round-robin selection
	for {
		wrr.currentIndex = (wrr.currentIndex + 1) % len(upstreams)

		if wrr.currentIndex == 0 {
			wrr.currentWeight = wrr.currentWeight - gcd
			if wrr.currentWeight <= 0 {
				wrr.currentWeight = maxWeight
			}
		}

		u := upstreams[wrr.currentIndex]
		if u.Weight() >= wrr.currentWeight {
			return u
		}
	}
}

// getAvailable returns available upstreams, excluding those in exclude map
// Returns main upstreams if any are available, otherwise fallback
func (wrr *WeightedRoundRobin) getAvailable(exclude map[string]bool) []*upstream.Upstream {
	// Try main upstreams first
	main := wrr.filterExcluded(wrr.provider.GetHealthyMain(), exclude)
	if len(main) > 0 {
		return main
	}

	// Fall back to fallback upstreams
	return wrr.filterExcluded(wrr.provider.GetHealthyFallback(), exclude)
}

// filterExcluded removes excluded upstreams from the list
func (wrr *WeightedRoundRobin) filterExcluded(upstreams []*upstream.Upstream, exclude map[string]bool) []*upstream.Upstream {
	if len(exclude) == 0 {
		return upstreams
	}

	result := make([]*upstream.Upstream, 0, len(upstreams))
	for _, u := range upstreams {
		if !exclude[u.Name()] {
			result = append(result, u)
		}
	}
	return result
}

// gcdWeights calculates the GCD of all upstream weights
func (wrr *WeightedRoundRobin) gcdWeights(upstreams []*upstream.Upstream) int {
	if len(upstreams) == 0 {
		return 1
	}

	result := upstreams[0].Weight()
	for i := 1; i < len(upstreams); i++ {
		result = gcd(result, upstreams[i].Weight())
	}
	return result
}

// maxWeight returns the maximum weight among upstreams
func (wrr *WeightedRoundRobin) maxWeight(upstreams []*upstream.Upstream) int {
	max := 0
	for _, u := range upstreams {
		if u.Weight() > max {
			max = u.Weight()
		}
	}
	return max
}

// gcd calculates the greatest common divisor
func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// Reset resets the balancer state
func (wrr *WeightedRoundRobin) Reset() {
	wrr.mu.Lock()
	defer wrr.mu.Unlock()

	wrr.currentIndex = -1
	wrr.currentWeight = 0
}

// Simple round-robin balancer (no weights)
type RoundRobin struct {
	provider UpstreamProvider
	mu       sync.Mutex
	index    int
}

// NewRoundRobin creates a new simple round-robin balancer
func NewRoundRobin(provider UpstreamProvider) *RoundRobin {
	return &RoundRobin{
		provider: provider,
		index:    -1,
	}
}

// Next returns the next upstream using simple round-robin
func (rr *RoundRobin) Next(exclude map[string]bool) *upstream.Upstream {
	rr.mu.Lock()
	defer rr.mu.Unlock()

	upstreams := rr.getAvailable(exclude)
	if len(upstreams) == 0 {
		return nil
	}

	rr.index = (rr.index + 1) % len(upstreams)
	return upstreams[rr.index]
}

// getAvailable returns available upstreams
func (rr *RoundRobin) getAvailable(exclude map[string]bool) []*upstream.Upstream {
	// Try main upstreams first
	main := rr.filterExcluded(rr.provider.GetHealthyMain(), exclude)
	if len(main) > 0 {
		return main
	}

	// Fall back to fallback upstreams
	return rr.filterExcluded(rr.provider.GetHealthyFallback(), exclude)
}

// filterExcluded removes excluded upstreams from the list
func (rr *RoundRobin) filterExcluded(upstreams []*upstream.Upstream, exclude map[string]bool) []*upstream.Upstream {
	if len(exclude) == 0 {
		return upstreams
	}

	result := make([]*upstream.Upstream, 0, len(upstreams))
	for _, u := range upstreams {
		if !exclude[u.Name()] {
			result = append(result, u)
		}
	}
	return result
}
