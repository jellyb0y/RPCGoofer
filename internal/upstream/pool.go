package upstream

import (
	"sync"

	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
)

// Pool represents a group of upstreams
type Pool struct {
	name      string
	upstreams []*Upstream
	monitor   *HealthMonitor
	logger    zerolog.Logger
	mu        sync.RWMutex
}

// NewPool creates a new Pool from a group configuration
func NewPool(groupCfg config.GroupConfig, globalCfg *config.Config, logger zerolog.Logger) *Pool {
	poolLogger := logger.With().Str("group", groupCfg.Name).Logger()

	upstreams := make([]*Upstream, 0, len(groupCfg.Upstreams))
	for _, upCfg := range groupCfg.Upstreams {
		u := NewUpstreamFromConfig(upCfg, globalCfg, poolLogger)
		upstreams = append(upstreams, u)
	}

	monitor := NewHealthMonitor(
		upstreams,
		globalCfg.BlockLagThreshold,
		globalCfg.GetBlockTimeoutDuration(),
		globalCfg.GetHealthCheckIntervalDuration(),
		globalCfg.GetStatusLogIntervalDuration(),
		poolLogger,
	)

	return &Pool{
		name:      groupCfg.Name,
		upstreams: upstreams,
		monitor:   monitor,
		logger:    poolLogger,
	}
}

// Name returns the pool name
func (p *Pool) Name() string {
	return p.name
}

// Start starts the health monitor
func (p *Pool) Start() {
	p.monitor.Start()
	p.logger.Info().
		Int("upstreams", len(p.upstreams)).
		Msg("pool started")
}

// Stop stops the pool and closes all connections
func (p *Pool) Stop() {
	p.monitor.Stop()
	for _, u := range p.upstreams {
		u.Close()
	}
	p.logger.Info().Msg("pool stopped")
}

// GetAll returns all upstreams
func (p *Pool) GetAll() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, len(p.upstreams))
	copy(result, p.upstreams)
	return result
}

// GetHealthy returns all healthy upstreams
func (p *Pool) GetHealthy() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, 0, len(p.upstreams))
	for _, u := range p.upstreams {
		if u.IsHealthy() {
			result = append(result, u)
		}
	}
	return result
}

// GetHealthyMain returns healthy main upstreams
func (p *Pool) GetHealthyMain() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, 0)
	for _, u := range p.upstreams {
		if u.IsHealthy() && u.IsMain() {
			result = append(result, u)
		}
	}
	return result
}

// GetHealthyFallback returns healthy fallback upstreams
func (p *Pool) GetHealthyFallback() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, 0)
	for _, u := range p.upstreams {
		if u.IsHealthy() && u.IsFallback() {
			result = append(result, u)
		}
	}
	return result
}

// GetAllHealthy returns all healthy upstreams (main + fallback)
// This is used for subscriptions where we want to subscribe to all
func (p *Pool) GetAllHealthy() []*Upstream {
	return p.GetHealthy()
}

// GetForRequest returns upstreams suitable for a request
// Returns main upstreams if any are healthy, otherwise returns fallback
func (p *Pool) GetForRequest() []*Upstream {
	main := p.GetHealthyMain()
	if len(main) > 0 {
		return main
	}
	return p.GetHealthyFallback()
}

// GetByName returns an upstream by name
func (p *Pool) GetByName(name string) *Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, u := range p.upstreams {
		if u.Name() == name {
			return u
		}
	}
	return nil
}

// HasHealthyUpstreams returns true if at least one upstream is healthy
func (p *Pool) HasHealthyUpstreams() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, u := range p.upstreams {
		if u.IsHealthy() {
			return true
		}
	}
	return false
}

// GetUpstreamCount returns the total number of upstreams
func (p *Pool) GetUpstreamCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.upstreams)
}

// GetHealthyCount returns the number of healthy upstreams
func (p *Pool) GetHealthyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, u := range p.upstreams {
		if u.IsHealthy() {
			count++
		}
	}
	return count
}

// GetWithWS returns upstreams that have WebSocket configured
func (p *Pool) GetWithWS() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, 0)
	for _, u := range p.upstreams {
		if u.HasWS() {
			result = append(result, u)
		}
	}
	return result
}

// GetHealthyWithWS returns healthy upstreams that have WebSocket configured
func (p *Pool) GetHealthyWithWS() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, 0)
	for _, u := range p.upstreams {
		if u.IsHealthy() && u.HasWS() {
			result = append(result, u)
		}
	}
	return result
}

// GetMainMaxBlock returns the maximum block number among healthy main upstreams
func (p *Pool) GetMainMaxBlock() uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var maxBlock uint64
	for _, u := range p.upstreams {
		if u.IsMain() && u.IsHealthy() {
			block := u.GetCurrentBlock()
			if block > maxBlock {
				maxBlock = block
			}
		}
	}
	return maxBlock
}

// HasHealthyMain returns true if at least one main upstream is healthy
func (p *Pool) HasHealthyMain() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, u := range p.upstreams {
		if u.IsMain() && u.IsHealthy() {
			return true
		}
	}
	return false
}
