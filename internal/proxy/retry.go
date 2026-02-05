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

	// All retries exhausted
	if lastResp != nil && lastResp.HasError() {
		return lastResp, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrAllUpstreamsFailed, lastErr)
	}

	return nil, ErrAllUpstreamsFailed
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

// ExecuteWithPool creates a temporary executor for a pool and executes.
// Upstreams that have not caught up to the requested block (for block-dependent methods) are excluded from selection.
func ExecuteWithPool(ctx context.Context, pool *upstream.Pool, req *jsonrpc.Request, cfg RetryConfig, logger zerolog.Logger) (*jsonrpc.Response, error) {
	var initialExclude map[string]bool
	if requestedBlock, ok := blockparam.GetRequestedBlockNumber(req.Method, req.Params); ok && requestedBlock > 0 {
		for _, u := range pool.GetForRequest() {
			if u.GetCurrentBlock() < requestedBlock {
				if initialExclude == nil {
					initialExclude = make(map[string]bool)
				}
				initialExclude[u.Name()] = true
			}
		}
	}
	bal := balancer.NewWeightedRoundRobin(pool)
	exec := NewExecutor(bal, pool, cfg, logger)
	return exec.Execute(ctx, req, initialExclude)
}

// ExecuteBatchWithPool creates a temporary executor for a pool and executes a batch.
// Upstreams that have not caught up to the max requested block in the batch are excluded from selection.
func ExecuteBatchWithPool(ctx context.Context, pool *upstream.Pool, requests []*jsonrpc.Request, cfg RetryConfig, logger zerolog.Logger) ([]*jsonrpc.Response, error) {
	var maxBlock uint64
	for _, req := range requests {
		if b, ok := blockparam.GetRequestedBlockNumber(req.Method, req.Params); ok && b > maxBlock {
			maxBlock = b
		}
	}
	var initialExclude map[string]bool
	if maxBlock > 0 {
		for _, u := range pool.GetForRequest() {
			if u.GetCurrentBlock() < maxBlock {
				if initialExclude == nil {
					initialExclude = make(map[string]bool)
				}
				initialExclude[u.Name()] = true
			}
		}
	}
	bal := balancer.NewWeightedRoundRobin(pool)
	exec := NewExecutor(bal, pool, cfg, logger)
	return exec.ExecuteBatch(ctx, requests, initialExclude)
}
