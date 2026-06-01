package proxy

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"rpcgofer/internal/balancer"
	"rpcgofer/internal/blockparam"
	"rpcgofer/internal/jsonrpc"
	"rpcgofer/internal/upstream"
)

// ErrAllUpstreamsFailed is returned when all upstreams fail
var ErrAllUpstreamsFailed = errors.New("all upstreams failed")

// ErrNoUpstreamsAvailable is returned when no upstreams are available
var ErrNoUpstreamsAvailable = errors.New("no upstreams available")

// context key for marking coalesced batch requests (set by HandlerExecutor.ExecuteBatch)
type coalescedBatchKeyType struct{}

var coalescedBatchKey = &coalescedBatchKeyType{}

// RetryConfig holds retry configuration
type RetryConfig struct {
	Enabled     bool
	MaxAttempts int
}

// Executor executes requests with retry logic
type Executor struct {
	balancer balancer.Selector
	pool     *upstream.Pool
	config   RetryConfig
	logger   zerolog.Logger
}

// NewExecutor creates a new Executor
func NewExecutor(b balancer.Selector, pool *upstream.Pool, cfg RetryConfig, logger zerolog.Logger) *Executor {
	return &Executor{
		balancer: b,
		pool:     pool,
		config:   cfg,
		logger:   logger,
	}
}

// Execute sends a request with retry logic.
// Tries all available upstreams (main first, then fallback) until success or all exhausted.
// initialExclude optionally pre-excludes upstreams (e.g. those that have not caught up to the requested block).
func (e *Executor) Execute(ctx context.Context, req *jsonrpc.Request, initialExclude map[string]bool) (*jsonrpc.Response, error) {
	tried := make(map[string]bool)
	if initialExclude != nil {
		for k, v := range initialExclude {
			tried[k] = v
		}
	}
	if !e.config.Enabled {
		resp, _, err := e.executeOnce(ctx, req, tried, false)
		return resp, err
	}
	var lastErr error
	var lastResp *jsonrpc.Response
	var lastUpstream string
	usedFallback := false
	selectedAny := false

	maxAttempts := e.config.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Check if we're now using fallback
		if !usedFallback && e.isUsingFallback(tried) {
			usedFallback = true
			e.logger.Warn().
				Str("method", req.Method).
				Int("triedMain", len(tried)).
				Msg("all main upstreams failed, falling back to fallback upstreams")
		}

		resp, upstreamName, err := e.executeOnce(ctx, req, tried, usedFallback)
		if upstreamName != "" {
			selectedAny = true
		}

		// Success - no error and no JSON-RPC error
		if err == nil && !resp.HasError() {
			return resp, nil
		}

		// Check if we should retry
		if err == nil && resp.HasError() {
			// JSON-RPC error - check if retryable
			if !resp.IsRetryableError() {
				// Non-retryable error, return immediately
				return resp, nil
			}
			lastResp = resp
			lastErr = fmt.Errorf("RPC error: %s", resp.Error.Message)
			lastUpstream = upstreamName
		} else if err != nil {
			lastErr = err
			lastUpstream = upstreamName
		}

		// Check if it's a context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Check if we have more upstreams to try
		if err == ErrNoUpstreamsAvailable {
			break
		}

		logEvent := e.logger.Warn().
			Int("attempt", attempt+1).
			Int("maxAttempts", maxAttempts).
			Err(lastErr).
			Str("method", req.Method).
			Bool("usingFallback", usedFallback)
		if lastUpstream != "" {
			logEvent = logEvent.Str("upstream", lastUpstream)
		}
		logEvent.Msg("request failed, retrying")
	}

	// Last-resort: no upstream was ever selectable (every candidate's circuit breaker is
	// open or was pre-excluded). If a block-healthy upstream is still reachable, try it
	// ignoring the breaker rather than reporting a total outage while an upstream works.
	if !selectedAny {
		if u := e.pickLastResort(tried); u != nil {
			e.logger.Warn().
				Str("method", req.Method).
				Str("upstream", u.Name()).
				Msg("all upstreams circuit-open, trying last-resort bypass")
			resp, err := u.ExecuteIgnoringCB(ctx, req)
			if err == nil {
				return resp, nil
			}
			lastErr = err
			lastUpstream = u.Name()
		}
	}

	// All retries exhausted
	if lastResp != nil && lastResp.HasError() {
		return lastResp, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrAllUpstreamsFailed, lastErr)
	}

	return nil, ErrAllUpstreamsFailed
}

// pickLastResort returns the highest-weight block-healthy upstream not already excluded,
// ignoring the circuit breaker. Used to avoid a full group outage when every upstream's
// breaker is open but at least one is still reachable. Returns nil if none qualifies.
func (e *Executor) pickLastResort(exclude map[string]bool) *upstream.Upstream {
	if e.pool == nil {
		return nil
	}
	var best *upstream.Upstream
	for _, u := range e.pool.GetForRequestIgnoringCB() {
		if exclude[u.Name()] {
			continue
		}
		if best == nil || u.Weight() > best.Weight() {
			best = u
		}
	}
	return best
}

// isUsingFallback checks if all main upstreams have been tried
func (e *Executor) isUsingFallback(tried map[string]bool) bool {
	if e.pool == nil {
		return false
	}
	mainUpstreams := e.pool.GetHealthyMain()
	for _, u := range mainUpstreams {
		if !tried[u.Name()] {
			return false
		}
	}
	return len(mainUpstreams) > 0
}

// executeOnce executes the request on a single upstream.
// Returns response, upstream name (empty if no upstream was selected), and error.
func (e *Executor) executeOnce(ctx context.Context, req *jsonrpc.Request, exclude map[string]bool, isFallback bool) (*jsonrpc.Response, string, error) {
	u := e.balancer.Next(exclude)
	if u == nil {
		return nil, "", ErrNoUpstreamsAvailable
	}

	upstreamName := u.Name()
	if exclude != nil {
		exclude[upstreamName] = true
	}

	resp, err := u.Execute(ctx, req)
	if err != nil {
		logEvent := e.logger.Warn().
			Err(err).
			Str("upstream", upstreamName).
			Str("method", req.Method).
			Bool("isFallback", u.IsFallback())

		if isFallback {
			logEvent.Msg("fallback request failed")
		} else {
			logEvent.Msg("request failed")
		}
		return nil, upstreamName, err
	}

	if ctx.Value(coalescedBatchKey) != nil {
		u.IncrementBatchCount()
	}

	if resp.HasError() {
		e.logger.Debug().
			Str("upstream", upstreamName).
			Str("method", req.Method).
			Int("errorCode", resp.Error.Code).
			Str("errorMessage", resp.Error.Message).
			Bool("isFallback", u.IsFallback()).
			Msg("RPC error response")
	} else {
		e.logger.Debug().
			Str("upstream", upstreamName).
			Str("method", req.Method).
			Bool("isFallback", u.IsFallback()).
			Msg("request succeeded")
	}

	return resp, upstreamName, nil
}

// ExecuteBatch sends a batch of requests with retry logic.
// initialExclude optionally pre-excludes upstreams (e.g. those that have not caught up to the max requested block).
func (e *Executor) ExecuteBatch(ctx context.Context, requests []*jsonrpc.Request, initialExclude map[string]bool) ([]*jsonrpc.Response, error) {
	tried := make(map[string]bool)
	if initialExclude != nil {
		for k, v := range initialExclude {
			tried[k] = v
		}
	}
	if !e.config.Enabled {
		responses, _, err := e.executeBatchOnce(ctx, requests, tried, false)
		return responses, err
	}
	var lastErr error
	var lastUpstream string
	usedFallback := false
	selectedAny := false

	maxAttempts := e.config.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Check if we're now using fallback
		if !usedFallback && e.isUsingFallback(tried) {
			usedFallback = true
			e.logger.Warn().
				Int("requests", len(requests)).
				Int("triedMain", len(tried)).
				Msg("all main upstreams failed for batch, falling back to fallback upstreams")
		}

		responses, upstreamName, err := e.executeBatchOnce(ctx, requests, tried, usedFallback)
		if upstreamName != "" {
			selectedAny = true
		}

		if err == nil {
			// Check if any response has a retryable error
			hasRetryable := false
			for _, resp := range responses {
				if resp.HasError() && resp.IsRetryableError() {
					hasRetryable = true
					break
				}
			}
			if !hasRetryable {
				return responses, nil
			}
			// Has retryable errors, continue
			lastErr = fmt.Errorf("batch has retryable errors")
			lastUpstream = upstreamName
		} else {
			lastErr = err
			lastUpstream = upstreamName
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if err == ErrNoUpstreamsAvailable {
			break
		}

		logEvent := e.logger.Warn().
			Int("attempt", attempt+1).
			Int("maxAttempts", maxAttempts).
			Err(lastErr).
			Int("requests", len(requests)).
			Bool("usingFallback", usedFallback)
		if lastUpstream != "" {
			logEvent = logEvent.Str("upstream", lastUpstream)
		}
		logEvent.Msg("batch request failed, retrying")
	}

	// Last-resort: no upstream was ever selectable (all circuit-open / pre-excluded).
	// Try a block-healthy upstream ignoring the breaker to avoid a full group outage.
	if !selectedAny {
		if u := e.pickLastResort(tried); u != nil && u.HasRPC() {
			e.logger.Warn().
				Int("requests", len(requests)).
				Str("upstream", u.Name()).
				Msg("all upstreams circuit-open, trying last-resort bypass for batch")
			responses, err := u.ExecuteBatchIgnoringCB(ctx, requests)
			if err == nil {
				return responses, nil
			}
			lastErr = err
			lastUpstream = u.Name()
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrAllUpstreamsFailed, lastErr)
	}

	return nil, ErrAllUpstreamsFailed
}

// executeBatchOnce executes a batch on a single upstream.
// Returns responses, upstream name (empty if no upstream was selected), and error.
func (e *Executor) executeBatchOnce(ctx context.Context, requests []*jsonrpc.Request, exclude map[string]bool, isFallback bool) ([]*jsonrpc.Response, string, error) {
	u := e.balancer.Next(exclude)
	if u == nil {
		return nil, "", ErrNoUpstreamsAvailable
	}

	upstreamName := u.Name()
	if exclude != nil {
		exclude[upstreamName] = true
	}

	// Check if upstream supports batch (HTTP only)
	if !u.HasRPC() {
		return nil, upstreamName, fmt.Errorf("upstream %s does not support batch requests", upstreamName)
	}

	responses, err := u.ExecuteBatch(ctx, requests)
	if err != nil {
		logEvent := e.logger.Warn().
			Err(err).
			Str("upstream", upstreamName).
			Int("requests", len(requests)).
			Bool("isFallback", u.IsFallback())

		if isFallback {
			logEvent.Msg("fallback batch request failed")
		} else {
			logEvent.Msg("batch request failed")
		}
		return nil, upstreamName, err
	}

	e.logger.Debug().
		Str("upstream", upstreamName).
		Int("requests", len(requests)).
		Int("responses", len(responses)).
		Bool("isFallback", u.IsFallback()).
		Msg("batch request succeeded")

	return responses, upstreamName, nil
}

// applyHistoricalAndLagExclude marks upstreams as excluded when:
//   - their currentBlock is below requestedBlock (lagging behind head);
//   - they declare a HistoricalBlockRange and requestedBlock is older than
//     currentBlock - HistoricalBlockRange (state was pruned for this block already).
//
// The lower-bound check uses minRequiredBlock — for single requests it equals requestedBlock,
// for batches it must be the OLDEST block in the batch (an upstream must serve all blocks).
//
// Returns the (possibly mutated) initialExclude map. Allocates the map lazily on first hit.
func applyHistoricalAndLagExclude(pool *upstream.Pool, requestedBlock, minRequiredBlock uint64, initialExclude map[string]bool) map[string]bool {
	if requestedBlock == 0 {
		return initialExclude
	}
	for _, u := range pool.GetForRequest() {
		if u.GetCurrentBlock() < requestedBlock {
			if initialExclude == nil {
				initialExclude = make(map[string]bool)
			}
			initialExclude[u.Name()] = true
			continue
		}
		if r := u.GetHistoricalRange(); r > 0 && minRequiredBlock > 0 {
			cur := u.GetCurrentBlock()
			// Guard against uint64 underflow when r > cur (e.g. node still syncing from genesis).
			if cur > r && minRequiredBlock+r < cur {
				if initialExclude == nil {
					initialExclude = make(map[string]bool)
				}
				initialExclude[u.Name()] = true
			}
		}
	}
	return initialExclude
}

// ExecuteWithPool creates a temporary executor for a pool and executes.
// Upstreams are excluded from selection when:
//   - the method is in their BlockedMethods list;
//   - their currentBlock is below the requested block (they haven't caught up to head);
//   - they declare a HistoricalBlockRange and the requested block is older than
//     currentBlock - HistoricalBlockRange (the upstream pruned state for this block already).
func ExecuteWithPool(ctx context.Context, pool *upstream.Pool, req *jsonrpc.Request, cfg RetryConfig, logger zerolog.Logger) (*jsonrpc.Response, error) {
	initialExclude := pool.BuildBlockedMethodsExclude(req.Method)
	if requestedBlock, ok := blockparam.GetRequestedBlockNumber(req.Method, req.Params); ok && requestedBlock > 0 {
		// For a single request, minRequiredBlock == requestedBlock.
		initialExclude = applyHistoricalAndLagExclude(pool, requestedBlock, requestedBlock, initialExclude)
	}
	bal := pool.GetSelector()
	exec := NewExecutor(bal, pool, cfg, logger)
	return exec.Execute(ctx, req, initialExclude)
}

// ExecuteBatchWithPool creates a temporary executor for a pool and executes a batch.
// Upstreams are excluded from selection when:
//   - any batch method is in their BlockedMethods list;
//   - their currentBlock is below the maximum requested block (block-lag);
//   - they declare a HistoricalBlockRange and the MINIMUM requested block in the batch
//     is older than currentBlock - HistoricalBlockRange. The minimum is used because the
//     upstream must serve every block in the batch — failing the oldest fails the whole batch.
func ExecuteBatchWithPool(ctx context.Context, pool *upstream.Pool, requests []*jsonrpc.Request, cfg RetryConfig, logger zerolog.Logger) ([]*jsonrpc.Response, error) {
	methods := make([]string, 0, len(requests))
	seen := make(map[string]bool)
	for _, req := range requests {
		if !seen[req.Method] {
			seen[req.Method] = true
			methods = append(methods, req.Method)
		}
	}
	initialExclude := pool.BuildBlockedMethodsExcludeForBatch(methods)
	var (
		maxBlock    uint64
		minBlock    uint64
		hasMinBlock bool
	)
	for _, req := range requests {
		if b, ok := blockparam.GetRequestedBlockNumber(req.Method, req.Params); ok && b > 0 {
			if b > maxBlock {
				maxBlock = b
			}
			if !hasMinBlock || b < minBlock {
				minBlock = b
				hasMinBlock = true
			}
		}
	}
	if maxBlock > 0 {
		minRequired := uint64(0)
		if hasMinBlock {
			minRequired = minBlock
		}
		initialExclude = applyHistoricalAndLagExclude(pool, maxBlock, minRequired, initialExclude)
	}
	bal := pool.GetSelector()
	exec := NewExecutor(bal, pool, cfg, logger)
	return exec.ExecuteBatch(ctx, requests, initialExclude)
}
