package upstream

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
)

// Selector selects an upstream for a request; exclude map filters out upstreams (e.g. by block or retry).
type Selector interface {
	Next(exclude map[string]bool) *Upstream
}

// Pool represents a group of upstreams
type Pool struct {
	name                  string
	upstreams              []*Upstream
	monitor               *HealthMonitor
	newHeadsProvider      NewHeadsProvider
	methodStats           *MethodStats
	selector              Selector
	logger                zerolog.Logger
	mu                    sync.RWMutex
	messageTimeout        time.Duration
	reconnectInterval     time.Duration
	pingInterval          time.Duration
	ctx                   context.Context
	cancel                context.CancelFunc
	onUpstreamWSConnected func(*Upstream)
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
		globalCfg.GetLagRecoveryTimeoutDuration(),
		globalCfg.GetHealthCheckIntervalDuration(),
		globalCfg.GetStatusLogIntervalDuration(),
		globalCfg.GetStatsLogIntervalDuration(),
		poolLogger,
	)

	methodStats := NewMethodStats()
	monitor.SetMethodStats(methodStats)

	return &Pool{
		name:              groupCfg.Name,
		upstreams:         upstreams,
		monitor:           monitor,
		methodStats:       methodStats,
		logger:            poolLogger,
		messageTimeout:    globalCfg.GetUpstreamMessageTimeoutDuration(),
		reconnectInterval: globalCfg.GetUpstreamReconnectIntervalDuration(),
		pingInterval:      globalCfg.GetUpstreamPingIntervalDuration(),
	}
}

// Name returns the pool name
func (p *Pool) Name() string {
	return p.name
}

// GetSelector returns the shared upstream selector for this pool
func (p *Pool) GetSelector() Selector {
	return p.selector
}

// SetSelector sets the upstream selector; call after pool creation (e.g. from server) to inject the balancer
func (p *Pool) SetSelector(s Selector) {
	p.selector = s
}

// SetOnUpstreamWSConnected sets a callback invoked when an upstream's WebSocket connects successfully.
func (p *Pool) SetOnUpstreamWSConnected(f func(*Upstream)) {
	p.onUpstreamWSConnected = f
}

// Start starts the WebSocket connections for upstreams with wsUrl, then the health monitor.
// WebSocket connections are retried in background until connected or pool is stopped.
func (p *Pool) Start() {
	p.ctx, p.cancel = context.WithCancel(context.Background())
	for _, u := range p.upstreams {
		if u.HasWS() {
			up := u
			go p.runWSRetry(up)
		}
	}
	p.monitor.Start()
	p.logger.Info().
		Int("upstreams", len(p.upstreams)).
		Msg("pool started")
}

// runWSRetry keeps trying to connect WebSocket in a loop with a short delay until success or context cancelled
func (p *Pool) runWSRetry(u *Upstream) {
	retryDelay := p.reconnectInterval
	if retryDelay < 3*time.Second {
		retryDelay = 3 * time.Second
	}
	p.logger.Info().Str("upstream", u.Name()).Dur("retryInterval", retryDelay).Msg("WebSocket connection loop started, will retry until connected")

	for {
		select {
		case <-p.ctx.Done():
			p.logger.Info().Str("upstream", u.Name()).Msg("WebSocket connection loop stopped (pool shutdown)")
			return
		default:
		}
		err := u.StartWS(p.ctx, p.messageTimeout, p.reconnectInterval, p.pingInterval)
		if err == nil {
			if p.onUpstreamWSConnected != nil {
				p.onUpstreamWSConnected(u)
			}
			p.logger.Info().Str("upstream", u.Name()).Msg("WebSocket connected, connection loop finished")
			return
		}
		p.logger.Warn().Err(err).Str("upstream", u.Name()).Dur("nextRetry", retryDelay).Msg("WebSocket connection failed, retrying")
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(retryDelay):
		}
	}
}

// Stop stops the pool and closes all connections
func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
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

// GetHealthyMain returns healthy main upstreams (excluding those with circuit breaker open)
func (p *Pool) GetHealthyMain() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, 0)
	for _, u := range p.upstreams {
		if u.IsHealthy() && u.IsMain() && u.AllowRequest() {
			result = append(result, u)
		}
	}
	return result
}

// GetHealthyFallback returns healthy fallback upstreams (excluding those with circuit breaker open)
func (p *Pool) GetHealthyFallback() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, 0)
	for _, u := range p.upstreams {
		if u.IsHealthy() && u.IsFallback() && u.AllowRequest() {
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

// GetConnectedWithWS returns upstreams that have WebSocket configured and are currently connected
func (p *Pool) GetConnectedWithWS() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Upstream, 0)
	for _, u := range p.upstreams {
		if u.HasWS() && u.IsWSConnected() {
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

// SetNewHeadsProvider sets the NewHeadsProvider for the health monitor
func (p *Pool) SetNewHeadsProvider(provider NewHeadsProvider) {
	p.newHeadsProvider = provider
	p.monitor.SetNewHeadsProvider(provider)
}

// SetBatchStats sets the BatchStatsProvider for the health monitor (for coalesced batch count in request statistics).
func (p *Pool) SetBatchStats(provider BatchStatsProvider) {
	p.monitor.SetBatchStats(provider)
}

// GetNewHeadsProvider returns the NewHeadsProvider
func (p *Pool) GetNewHeadsProvider() NewHeadsProvider {
	return p.newHeadsProvider
}

// GetBlockNotifyChan returns the channel that signals when a new max block is seen
func (p *Pool) GetBlockNotifyChan() <-chan struct{} {
	return p.monitor.BlockNotifyChan()
}

// RecordMethod increments the method call counter
func (p *Pool) RecordMethod(method string) {
	p.methodStats.Increment(method)
}

// RecordMethods increments method call counters for multiple methods
func (p *Pool) RecordMethods(methods []string) {
	for _, method := range methods {
		p.methodStats.Increment(method)
	}
}

// GetMethodStats returns the method stats
func (p *Pool) GetMethodStats() *MethodStats {
	return p.methodStats
}

// BuildBlockedMethodsExclude returns upstream names to exclude for the given method.
// Upstreams that have this method in their blockedMethods list will be excluded.
func (p *Pool) BuildBlockedMethodsExclude(method string) map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	exclude := make(map[string]bool)
	for _, u := range p.upstreams {
		if u.IsMethodBlocked(method) {
			exclude[u.Name()] = true
		}
	}
	return exclude
}

// BuildBlockedMethodsExcludeForBatch returns upstream names to exclude for the given methods.
// Upstreams that block any of the methods will be excluded (batch goes to one upstream).
func (p *Pool) BuildBlockedMethodsExcludeForBatch(methods []string) map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	exclude := make(map[string]bool)
	for _, u := range p.upstreams {
		for _, m := range methods {
			if u.IsMethodBlocked(m) {
				exclude[u.Name()] = true
				break
			}
		}
	}
	return exclude
}
