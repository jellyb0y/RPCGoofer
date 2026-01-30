package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/jsonrpc"
)

// HealthMonitor monitors the health of upstreams in a group
type HealthMonitor struct {
	upstreams          []*Upstream
	upstreamsByName    map[string]*Upstream
	blockLagThreshold  uint64
	lagRecoveryTimeout time.Duration
	checkInterval      time.Duration
	statusLogInterval  time.Duration
	statsLogInterval   time.Duration
	logger             zerolog.Logger

	newHeadsProvider NewHeadsProvider

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.RWMutex
	maxBlock uint64
}

// NewHealthMonitor creates a new HealthMonitor
func NewHealthMonitor(upstreams []*Upstream, blockLagThreshold uint64, lagRecoveryTimeout time.Duration, checkInterval time.Duration, statusLogInterval time.Duration, statsLogInterval time.Duration, logger zerolog.Logger) *HealthMonitor {
	ctx, cancel := context.WithCancel(context.Background())

	// Build upstream name lookup map
	upstreamsByName := make(map[string]*Upstream, len(upstreams))
	for _, u := range upstreams {
		upstreamsByName[u.Name()] = u
	}

	return &HealthMonitor{
		upstreams:          upstreams,
		upstreamsByName:    upstreamsByName,
		blockLagThreshold:  blockLagThreshold,
		lagRecoveryTimeout: lagRecoveryTimeout,
		checkInterval:      checkInterval,
		statusLogInterval:  statusLogInterval,
		statsLogInterval:   statsLogInterval,
		logger:             logger,
		ctx:                ctx,
		cancel:             cancel,
	}
}

// SetNewHeadsProvider sets the provider for newHeads events
func (hm *HealthMonitor) SetNewHeadsProvider(provider NewHeadsProvider) {
	hm.newHeadsProvider = provider
}

// ID implements NewHeadsSubscriber interface
func (hm *HealthMonitor) ID() string {
	return "health-monitor"
}

// OnBlock implements NewHeadsSubscriber interface
func (hm *HealthMonitor) OnBlock(upstreamName string, result json.RawMessage) {
	u, ok := hm.upstreamsByName[upstreamName]
	if !ok {
		hm.logger.Warn().Str("upstream", upstreamName).Msg("received block from unknown upstream")
		return
	}

	var header jsonrpc.BlockHeader
	if err := json.Unmarshal(result, &header); err != nil {
		hm.logger.Warn().Err(err).Msg("failed to parse block header")
		return
	}

	blockNum, err := parseHexUint64(header.Number)
	if err != nil {
		hm.logger.Warn().Err(err).Str("number", header.Number).Msg("failed to parse block number")
		return
	}

	u.UpdateBlock(blockNum)
	u.SetHealthy(true)
	isNewMax := hm.updateMaxBlock(blockNum)

	if isNewMax {
		// New max block - give other upstreams time to catch up
		hm.scheduleLagCheck(blockNum)
	}

	// Check if this upstream caught up
	hm.checkUpstreamCaughtUp(u)

	hm.logger.Debug().
		Str("upstream", u.Name()).
		Uint64("block", blockNum).
		Str("hash", header.Hash).
		Msg("new block from shared subscription")
}

// fetchInitialBlocks fetches the current block number from all upstreams in parallel
func (hm *HealthMonitor) fetchInitialBlocks() {
	var wg sync.WaitGroup

	for _, u := range hm.upstreams {
		if !u.HasRPC() {
			continue
		}
		wg.Add(1)
		go func(u *Upstream) {
			defer wg.Done()
			hm.pollBlockNumber(u)
		}(u)
	}

	wg.Wait()

	// Log initial blocks for all upstreams
	hm.mu.RLock()
	maxBlock := hm.maxBlock
	hm.mu.RUnlock()

	for _, u := range hm.upstreams {
		block := u.GetCurrentBlock()
		healthy := u.IsHealthy()

		if block > 0 {
			hm.logger.Info().
				Str("upstream", u.Name()).
				Uint64("block", block).
				Bool("healthy", healthy).
				Msg("fetched initial block")
		} else {
			hm.logger.Warn().
				Str("upstream", u.Name()).
				Bool("healthy", healthy).
				Msg("failed to fetch initial block")
		}
	}

	hm.logger.Info().
		Uint64("maxBlock", maxBlock).
		Int("upstreams", len(hm.upstreams)).
		Msg("initial blocks fetched")
}

// Start begins health monitoring
func (hm *HealthMonitor) Start() {
	// Fetch initial blocks from all upstreams
	hm.fetchInitialBlocks()

	// If we have a shared subscription provider, use it for newHeads
	if hm.newHeadsProvider != nil {
		// Subscribe to newHeads through the shared subscription manager
		if err := hm.newHeadsProvider.SubscribeNewHeads(hm.ctx, hm); err != nil {
			hm.logger.Warn().Err(err).Msg("failed to subscribe to newHeads via shared subscription, falling back to polling")
			// Fall back to polling for all upstreams
			for _, u := range hm.upstreams {
				if u.HasRPC() {
					hm.wg.Add(1)
					go hm.monitorWithPolling(u)
				}
			}
		} else {
			hm.logger.Info().Msg("subscribed to newHeads via shared subscription manager")
			// Only poll upstreams that don't have WebSocket
			for _, u := range hm.upstreams {
				if !u.HasWS() && u.HasRPC() {
					hm.wg.Add(1)
					go hm.monitorWithPolling(u)
				}
			}
		}
	} else {
		// No shared subscription provider - poll all upstreams
		for _, u := range hm.upstreams {
			if u.HasRPC() {
				hm.wg.Add(1)
				go hm.monitorWithPolling(u)
			}
		}
	}

	// Start status logging goroutine
	if hm.statusLogInterval > 0 {
		hm.wg.Add(1)
		go hm.logStatus()
	}

	// Start request statistics logging goroutine
	if hm.statsLogInterval > 0 {
		hm.wg.Add(1)
		go hm.logRequestStats()
	}
}

// logStatus periodically logs the status of all upstreams
func (hm *HealthMonitor) logStatus() {
	defer hm.wg.Done()

	ticker := time.NewTicker(hm.statusLogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-hm.ctx.Done():
			return
		case <-ticker.C:
			hm.logCurrentStatus()
		}
	}
}

// logCurrentStatus logs the current status of all upstreams
func (hm *HealthMonitor) logCurrentStatus() {
	hm.mu.RLock()
	maxBlock := hm.maxBlock
	hm.mu.RUnlock()

	var healthyMain, unhealthyMain, healthyFallback, unhealthyFallback []string

	for _, u := range hm.upstreams {
		block := u.GetCurrentBlock()
		healthy := u.IsHealthy()
		name := u.Name()

		status := fmt.Sprintf("%s(block=%d)", name, block)

		if u.IsMain() {
			if healthy {
				healthyMain = append(healthyMain, status)
			} else {
				unhealthyMain = append(unhealthyMain, status)
			}
		} else {
			if healthy {
				healthyFallback = append(healthyFallback, status)
			} else {
				unhealthyFallback = append(unhealthyFallback, status)
			}
		}
	}

	hm.logger.Info().
		Uint64("maxBlock", maxBlock).
		Strs("healthyMain", healthyMain).
		Strs("unhealthyMain", unhealthyMain).
		Strs("healthyFallback", healthyFallback).
		Strs("unhealthyFallback", unhealthyFallback).
		Msg("upstreams status")
}

// logRequestStats periodically logs the request statistics of all upstreams
func (hm *HealthMonitor) logRequestStats() {
	defer hm.wg.Done()

	ticker := time.NewTicker(hm.statsLogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-hm.ctx.Done():
			return
		case <-ticker.C:
			hm.logCurrentRequestStats()
		}
	}
}

// logCurrentRequestStats logs the current request statistics and resets counters
func (hm *HealthMonitor) logCurrentRequestStats() {
	var totalRequests uint64
	requestStats := make(map[string]uint64)

	for _, u := range hm.upstreams {
		count := u.SwapRequestCount()
		requestStats[u.Name()] = count
		totalRequests += count
	}

	// Build log event with stats for each upstream
	logEvent := hm.logger.Info().
		Uint64("totalRequests", totalRequests).
		Dur("interval", hm.statsLogInterval)

	for _, u := range hm.upstreams {
		logEvent = logEvent.Uint64(u.Name(), requestStats[u.Name()])
	}

	logEvent.Msg("request statistics")
}

// Stop stops health monitoring
func (hm *HealthMonitor) Stop() {
	// Unsubscribe from shared subscription if we have a provider
	if hm.newHeadsProvider != nil {
		if err := hm.newHeadsProvider.UnsubscribeNewHeads(hm.ID()); err != nil {
			hm.logger.Warn().Err(err).Msg("failed to unsubscribe from newHeads")
		}
	}

	hm.cancel()
	hm.wg.Wait()
}

// monitorWithPolling monitors an upstream using periodic eth_blockNumber calls
func (hm *HealthMonitor) monitorWithPolling(u *Upstream) {
	defer hm.wg.Done()

	ticker := time.NewTicker(hm.checkInterval)
	defer ticker.Stop()

	// Initial check
	hm.pollBlockNumber(u)

	for {
		select {
		case <-hm.ctx.Done():
			return
		case <-ticker.C:
			hm.pollBlockNumber(u)
		}
	}
}

// pollBlockNumber fetches the current block number via HTTP
func (hm *HealthMonitor) pollBlockNumber(u *Upstream) {
	ctx, cancel := context.WithTimeout(hm.ctx, 10*time.Second)
	defer cancel()

	req, err := jsonrpc.NewRequest("eth_blockNumber", nil, jsonrpc.NewIDInt(1))
	if err != nil {
		hm.logger.Warn().Err(err).Str("upstream", u.Name()).Msg("failed to create request")
		u.SetHealthy(false)
		return
	}

	resp, err := u.ExecuteHTTP(ctx, req)
	if err != nil {
		hm.logger.Warn().Err(err).Str("upstream", u.Name()).Msg("failed to get block number")
		u.SetHealthy(false)
		return
	}

	if resp.HasError() {
		hm.logger.Warn().
			Str("upstream", u.Name()).
			Int("code", resp.Error.Code).
			Str("message", resp.Error.Message).
			Msg("error getting block number")
		u.SetHealthy(false)
		return
	}

	var blockNumHex string
	if err := json.Unmarshal(resp.Result, &blockNumHex); err != nil {
		hm.logger.Warn().Err(err).Str("upstream", u.Name()).Msg("failed to parse block number")
		u.SetHealthy(false)
		return
	}

	blockNum, err := parseHexUint64(blockNumHex)
	if err != nil {
		hm.logger.Warn().Err(err).Str("upstream", u.Name()).Msg("failed to parse block number hex")
		u.SetHealthy(false)
		return
	}

	u.UpdateBlock(blockNum)
	isNewMax := hm.updateMaxBlock(blockNum)

	if isNewMax {
		// New max block - give other upstreams time to catch up
		hm.scheduleLagCheck(blockNum)
	}

	// Check if this upstream caught up
	hm.checkUpstreamCaughtUp(u)

	hm.logger.Debug().
		Str("upstream", u.Name()).
		Uint64("block", blockNum).
		Msg("polled block number")
}

// updateMaxBlock updates the maximum block number seen across all upstreams
// Returns true if this is a new maximum block
func (hm *HealthMonitor) updateMaxBlock(block uint64) bool {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if block > hm.maxBlock {
		hm.maxBlock = block
		return true
	}
	return false
}

// GetMaxBlock returns the maximum block number
func (hm *HealthMonitor) GetMaxBlock() uint64 {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	return hm.maxBlock
}

// scheduleLagCheck schedules a health check for a specific block after lagRecoveryTimeout
func (hm *HealthMonitor) scheduleLagCheck(targetBlock uint64) {
	if hm.lagRecoveryTimeout <= 0 {
		return
	}

	time.AfterFunc(hm.lagRecoveryTimeout, func() {
		// Check if context is still valid
		select {
		case <-hm.ctx.Done():
			return
		default:
		}
		hm.checkLagForBlock(targetBlock)
	})
}

// checkLagForBlock checks all upstreams against a specific target block
// Marks upstreams as unhealthy if they haven't caught up to the target block
func (hm *HealthMonitor) checkLagForBlock(targetBlock uint64) {
	for _, u := range hm.upstreams {
		currentBlock := u.GetCurrentBlock()

		if targetBlock > currentBlock {
			lag := targetBlock - currentBlock
			if lag > hm.blockLagThreshold {
				if u.IsHealthy() {
					hm.logger.Warn().
						Str("upstream", u.Name()).
						Uint64("currentBlock", currentBlock).
						Uint64("targetBlock", targetBlock).
						Uint64("lag", lag).
						Msg("upstream did not catch up in time, marking unhealthy")
				}
				u.SetHealthy(false)
			}
		}
	}
}

// checkUpstreamCaughtUp checks if an upstream has caught up to the current max block
// and marks it as healthy if the lag is within threshold
func (hm *HealthMonitor) checkUpstreamCaughtUp(u *Upstream) {
	hm.mu.RLock()
	maxBlock := hm.maxBlock
	hm.mu.RUnlock()

	currentBlock := u.GetCurrentBlock()

	if maxBlock == 0 || currentBlock == 0 {
		return
	}

	var lag uint64
	if maxBlock > currentBlock {
		lag = maxBlock - currentBlock
	}

	if lag <= hm.blockLagThreshold {
		if !u.IsHealthy() {
			hm.logger.Info().
				Str("upstream", u.Name()).
				Uint64("currentBlock", currentBlock).
				Uint64("maxBlock", maxBlock).
				Msg("upstream caught up, marking healthy")
		}
		u.SetHealthy(true)
	}
}

// RefreshAllHealth recalculates health for all upstreams
func (hm *HealthMonitor) RefreshAllHealth() {
	// First, recalculate max block
	var maxBlock uint64
	for _, u := range hm.upstreams {
		block := u.GetCurrentBlock()
		if block > maxBlock {
			maxBlock = block
		}
	}

	hm.mu.Lock()
	hm.maxBlock = maxBlock
	hm.mu.Unlock()

	// Then check if each upstream has caught up
	for _, u := range hm.upstreams {
		hm.checkUpstreamCaughtUp(u)
	}
}

// parseHexUint64 parses a hex string (with 0x prefix) to uint64
func parseHexUint64(hex string) (uint64, error) {
	hex = strings.TrimPrefix(hex, "0x")
	return strconv.ParseUint(hex, 16, 64)
}
