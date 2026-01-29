package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"rpcgofer/internal/jsonrpc"
)

// HealthMonitor monitors the health of upstreams in a group
type HealthMonitor struct {
	upstreams         []*Upstream
	blockLagThreshold uint64
	blockTimeout      time.Duration
	checkInterval     time.Duration
	statusLogInterval time.Duration
	logger            zerolog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.RWMutex
	maxBlock uint64
}

// NewHealthMonitor creates a new HealthMonitor
func NewHealthMonitor(upstreams []*Upstream, blockLagThreshold uint64, blockTimeout time.Duration, checkInterval time.Duration, statusLogInterval time.Duration, logger zerolog.Logger) *HealthMonitor {
	ctx, cancel := context.WithCancel(context.Background())

	return &HealthMonitor{
		upstreams:         upstreams,
		blockLagThreshold: blockLagThreshold,
		blockTimeout:      blockTimeout,
		checkInterval:     checkInterval,
		statusLogInterval: statusLogInterval,
		logger:            logger,
		ctx:               ctx,
		cancel:            cancel,
	}
}

// Start begins health monitoring
func (hm *HealthMonitor) Start() {
	for _, u := range hm.upstreams {
		if u.HasWS() {
			hm.wg.Add(1)
			go hm.monitorWithWS(u)
		} else if u.HasRPC() {
			hm.wg.Add(1)
			go hm.monitorWithPolling(u)
		}
	}

	// Start status logging goroutine
	if hm.statusLogInterval > 0 {
		hm.wg.Add(1)
		go hm.logStatus()
	}

	// Start block timeout checker
	if hm.blockTimeout > 0 {
		hm.wg.Add(1)
		go hm.checkBlockTimeouts()
	}
}

// checkBlockTimeouts periodically checks if upstreams have timed out
func (hm *HealthMonitor) checkBlockTimeouts() {
	defer hm.wg.Done()

	// Check more frequently than the timeout itself
	checkInterval := hm.blockTimeout / 4
	if checkInterval < 500*time.Millisecond {
		checkInterval = 500 * time.Millisecond
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-hm.ctx.Done():
			return
		case <-ticker.C:
			hm.checkAllTimeouts()
		}
	}
}

// checkAllTimeouts checks all upstreams for block timeout
func (hm *HealthMonitor) checkAllTimeouts() {
	now := time.Now()

	for _, u := range hm.upstreams {
		lastBlockTime := u.GetLastBlockTime()

		// Skip if no block received yet
		if lastBlockTime.IsZero() {
			continue
		}

		timeSinceBlock := now.Sub(lastBlockTime)
		if timeSinceBlock > hm.blockTimeout {
			if u.IsHealthy() {
				hm.logger.Warn().
					Str("upstream", u.Name()).
					Dur("timeSinceBlock", timeSinceBlock).
					Dur("timeout", hm.blockTimeout).
					Msg("upstream block timeout, marking unhealthy")
				u.SetHealthy(false)
			}
		}
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

// Stop stops health monitoring
func (hm *HealthMonitor) Stop() {
	hm.cancel()
	hm.wg.Wait()
}

// monitorWithWS monitors an upstream using WebSocket subscription to newHeads
func (hm *HealthMonitor) monitorWithWS(u *Upstream) {
	defer hm.wg.Done()

	for {
		select {
		case <-hm.ctx.Done():
			return
		default:
		}

		if err := hm.subscribeNewHeads(u); err != nil {
			hm.logger.Warn().Err(err).Str("upstream", u.Name()).Msg("newHeads subscription failed, reconnecting...")
			u.SetHealthy(false)
			time.Sleep(5 * time.Second)
			continue
		}
	}
}

// subscribeNewHeads subscribes to newHeads and processes blocks
func (hm *HealthMonitor) subscribeNewHeads(u *Upstream) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(hm.ctx, u.WSURL(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	// Subscribe to newHeads
	subReq, err := jsonrpc.NewRequest("eth_subscribe", []string{"newHeads"}, jsonrpc.NewIDInt(1))
	if err != nil {
		return fmt.Errorf("failed to create subscribe request: %w", err)
	}

	reqBytes, err := subReq.Bytes()
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, reqBytes); err != nil {
		return fmt.Errorf("failed to send subscribe request: %w", err)
	}

	// Read subscription response
	_, respData, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("failed to read subscribe response: %w", err)
	}

	var subResp jsonrpc.Response
	if err := json.Unmarshal(respData, &subResp); err != nil {
		return fmt.Errorf("failed to parse subscribe response: %w", err)
	}

	if subResp.HasError() {
		return fmt.Errorf("subscribe error: %s", subResp.Error.Message)
	}

	hm.logger.Debug().Str("upstream", u.Name()).Msg("subscribed to newHeads")
	u.SetHealthy(true)

	// Read newHeads notifications
	for {
		select {
		case <-hm.ctx.Done():
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("failed to read message: %w", err)
		}

		var notification jsonrpc.SubscriptionNotification
		if err := json.Unmarshal(data, &notification); err != nil {
			continue // Not a notification, skip
		}

		if notification.Method != "eth_subscription" {
			continue
		}

		var header jsonrpc.BlockHeader
		if err := json.Unmarshal(notification.Params.Result, &header); err != nil {
			hm.logger.Warn().Err(err).Msg("failed to parse block header")
			continue
		}

		blockNum, err := parseHexUint64(header.Number)
		if err != nil {
			hm.logger.Warn().Err(err).Str("number", header.Number).Msg("failed to parse block number")
			continue
		}

		u.UpdateBlock(blockNum)
		hm.updateMaxBlock(blockNum)
		hm.updateHealthStatus(u)

		hm.logger.Debug().
			Str("upstream", u.Name()).
			Uint64("block", blockNum).
			Str("hash", header.Hash).
			Msg("new block")
	}
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
	hm.updateMaxBlock(blockNum)
	hm.updateHealthStatus(u)

	hm.logger.Debug().
		Str("upstream", u.Name()).
		Uint64("block", blockNum).
		Msg("polled block number")
}

// updateMaxBlock updates the maximum block number seen across all upstreams
func (hm *HealthMonitor) updateMaxBlock(block uint64) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if block > hm.maxBlock {
		hm.maxBlock = block
	}
}

// GetMaxBlock returns the maximum block number
func (hm *HealthMonitor) GetMaxBlock() uint64 {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	return hm.maxBlock
}

// updateHealthStatus updates the health status of an upstream based on block lag
func (hm *HealthMonitor) updateHealthStatus(u *Upstream) {
	hm.mu.RLock()
	maxBlock := hm.maxBlock
	hm.mu.RUnlock()

	currentBlock := u.GetCurrentBlock()

	if maxBlock == 0 || currentBlock == 0 {
		// Not enough data yet, assume healthy
		u.SetHealthy(true)
		return
	}

	lag := maxBlock - currentBlock
	healthy := lag <= hm.blockLagThreshold

	if !healthy && u.IsHealthy() {
		hm.logger.Warn().
			Str("upstream", u.Name()).
			Uint64("currentBlock", currentBlock).
			Uint64("maxBlock", maxBlock).
			Uint64("lag", lag).
			Msg("upstream is lagging, marking unhealthy")
	} else if healthy && !u.IsHealthy() {
		hm.logger.Info().
			Str("upstream", u.Name()).
			Uint64("currentBlock", currentBlock).
			Uint64("maxBlock", maxBlock).
			Msg("upstream recovered, marking healthy")
	}

	u.SetHealthy(healthy)
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

	// Then update health for each upstream
	for _, u := range hm.upstreams {
		hm.updateHealthStatus(u)
	}
}

// parseHexUint64 parses a hex string (with 0x prefix) to uint64
func parseHexUint64(hex string) (uint64, error) {
	hex = strings.TrimPrefix(hex, "0x")
	return strconv.ParseUint(hex, 16, 64)
}
