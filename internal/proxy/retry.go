package proxy

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"rpcgofer/internal/balancer"
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

// Execute sends a request with retry logic
// Tries all available upstreams (main first, then fallback) until success or all exhausted
func (e *Executor) Execute(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Response, error) {
	if !e.config.Enabled {
		return e.executeOnce(ctx, req, nil, false)
	}

	tried := make(map[string]bool)
	var lastErr error
	var lastResp *jsonrpc.Response
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

		resp, err := e.executeOnce(ctx, req, tried, usedFallback)

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
		} else if err != nil {
			lastErr = err
		}

		// Check if it's a context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Check if we have more upstreams to try
		if err == ErrNoUpstreamsAvailable {
			break
		}

		e.logger.Warn().
			Int("attempt", attempt+1).
			Int("maxAttempts", maxAttempts).
			Err(lastErr).
			Str("method", req.Method).
			Bool("usingFallback", usedFallback).
			Msg("request failed, retrying")
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

// executeOnce executes the request on a single upstream
func (e *Executor) executeOnce(ctx context.Context, req *jsonrpc.Request, exclude map[string]bool, isFallback bool) (*jsonrpc.Response, error) {
	u := e.balancer.Next(exclude)
	if u == nil {
		return nil, ErrNoUpstreamsAvailable
	}

	if exclude != nil {
		exclude[u.Name()] = true
	}

	resp, err := u.Execute(ctx, req)
	if err != nil {
		logEvent := e.logger.Warn().
			Err(err).
			Str("upstream", u.Name()).
			Str("method", req.Method).
			Bool("isFallback", u.IsFallback())
		
		if isFallback {
			logEvent.Msg("fallback request failed")
		} else {
			logEvent.Msg("request failed")
		}
		return nil, err
	}

	if resp.HasError() {
		e.logger.Debug().
			Str("upstream", u.Name()).
			Str("method", req.Method).
			Int("errorCode", resp.Error.Code).
			Str("errorMessage", resp.Error.Message).
			Bool("isFallback", u.IsFallback()).
			Msg("RPC error response")
	} else {
		e.logger.Debug().
			Str("upstream", u.Name()).
			Str("method", req.Method).
			Bool("isFallback", u.IsFallback()).
			Msg("request succeeded")
	}

	return resp, nil
}

// ExecuteBatch sends a batch of requests with retry logic
func (e *Executor) ExecuteBatch(ctx context.Context, requests []*jsonrpc.Request) ([]*jsonrpc.Response, error) {
	if !e.config.Enabled {
		return e.executeBatchOnce(ctx, requests, nil, false)
	}

	tried := make(map[string]bool)
	var lastErr error
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

		responses, err := e.executeBatchOnce(ctx, requests, tried, usedFallback)

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
		} else {
			lastErr = err
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if err == ErrNoUpstreamsAvailable {
			break
		}

		e.logger.Warn().
			Int("attempt", attempt+1).
			Int("maxAttempts", maxAttempts).
			Err(lastErr).
			Int("requests", len(requests)).
			Bool("usingFallback", usedFallback).
			Msg("batch request failed, retrying")
	}

	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrAllUpstreamsFailed, lastErr)
	}

	return nil, ErrAllUpstreamsFailed
}

// executeBatchOnce executes a batch on a single upstream
func (e *Executor) executeBatchOnce(ctx context.Context, requests []*jsonrpc.Request, exclude map[string]bool, isFallback bool) ([]*jsonrpc.Response, error) {
	u := e.balancer.Next(exclude)
	if u == nil {
		return nil, ErrNoUpstreamsAvailable
	}

	if exclude != nil {
		exclude[u.Name()] = true
	}

	// Check if upstream supports batch (HTTP only)
	if !u.HasRPC() {
		return nil, fmt.Errorf("upstream %s does not support batch requests", u.Name())
	}

	responses, err := u.ExecuteBatch(ctx, requests)
	if err != nil {
		logEvent := e.logger.Warn().
			Err(err).
			Str("upstream", u.Name()).
			Int("requests", len(requests)).
			Bool("isFallback", u.IsFallback())
		
		if isFallback {
			logEvent.Msg("fallback batch request failed")
		} else {
			logEvent.Msg("batch request failed")
		}
		return nil, err
	}

	e.logger.Debug().
		Str("upstream", u.Name()).
		Int("requests", len(requests)).
		Int("responses", len(responses)).
		Bool("isFallback", u.IsFallback()).
		Msg("batch request succeeded")

	return responses, nil
}

// ExecuteWithPool creates a temporary executor for a pool and executes
func ExecuteWithPool(ctx context.Context, pool *upstream.Pool, req *jsonrpc.Request, cfg RetryConfig, logger zerolog.Logger) (*jsonrpc.Response, error) {
	b := balancer.NewWeightedRoundRobin(pool)
	exec := NewExecutor(b, pool, cfg, logger)
	return exec.Execute(ctx, req)
}

// ExecuteBatchWithPool creates a temporary executor for a pool and executes a batch
func ExecuteBatchWithPool(ctx context.Context, pool *upstream.Pool, requests []*jsonrpc.Request, cfg RetryConfig, logger zerolog.Logger) ([]*jsonrpc.Response, error) {
	b := balancer.NewWeightedRoundRobin(pool)
	exec := NewExecutor(b, pool, cfg, logger)
	return exec.ExecuteBatch(ctx, requests)
}
