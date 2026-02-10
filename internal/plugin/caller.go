package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog"

	"rpcgofer/internal/balancer"
	"rpcgofer/internal/jsonrpc"
	"rpcgofer/internal/upstream"
)

// PoolCaller implements UpstreamCaller using upstream.Pool
type PoolCaller struct {
	ctx         context.Context
	pool        *upstream.Pool
	retryConfig RetryConfig
	logger      zerolog.Logger
}

// RetryConfig holds retry configuration for plugin calls
type RetryConfig struct {
	Enabled     bool
	MaxAttempts int
}

// NewPoolCaller creates a new PoolCaller
func NewPoolCaller(ctx context.Context, pool *upstream.Pool, retryCfg RetryConfig, logger zerolog.Logger) *PoolCaller {
	return &PoolCaller{
		ctx:         ctx,
		pool:        pool,
		retryConfig: retryCfg,
		logger:      logger.With().Str("component", "plugin-caller").Logger(),
	}
}

// Call executes a single RPC call to upstream
func (c *PoolCaller) Call(method string, params interface{}) (json.RawMessage, error) {
	// Build JSON-RPC request
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      jsonrpc.NewIDInt(1),
		Method:  method,
		Params:  paramsRaw,
	}

	// Execute with retry logic
	resp, err := c.executeWithRetry(req)
	if err != nil {
		return nil, err
	}

	if resp.HasError() {
		return nil, fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	return resp.Result, nil
}

// BatchCall executes multiple RPC calls to upstream
func (c *PoolCaller) BatchCall(calls []CallRequest) ([]json.RawMessage, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	// Build JSON-RPC requests
	requests := make([]*jsonrpc.Request, len(calls))
	for i, call := range calls {
		paramsRaw, err := json.Marshal(call.Params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params for call %d: %w", i, err)
		}

		requests[i] = &jsonrpc.Request{
			JSONRPC: jsonrpc.Version,
			ID:      jsonrpc.NewIDInt(int64(i + 1)),
			Method:  call.Method,
			Params:  paramsRaw,
		}
	}

	// Execute batch with retry
	responses, err := c.executeBatchWithRetry(requests)
	if err != nil {
		return nil, err
	}

	// Extract results
	results := make([]json.RawMessage, len(responses))
	for i, resp := range responses {
		if resp.HasError() {
			// Return error as JSON for the plugin to handle
			errJSON, _ := json.Marshal(map[string]interface{}{
				"error": map[string]interface{}{
					"code":    resp.Error.Code,
					"message": resp.Error.Message,
				},
			})
			results[i] = errJSON
		} else {
			results[i] = resp.Result
		}
	}

	return results, nil
}

// executeWithRetry executes a single request with retry logic
func (c *PoolCaller) executeWithRetry(req *jsonrpc.Request) (*jsonrpc.Response, error) {
	b := c.pool.GetSelector()

	if !c.retryConfig.Enabled {
		return c.executeOnce(b, req, c.pool.BuildBlockedMethodsExclude(req.Method))
	}

	tried := c.pool.BuildBlockedMethodsExclude(req.Method)
	var lastErr error
	var lastResp *jsonrpc.Response

	maxAttempts := c.retryConfig.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := c.executeOnce(b, req, tried)

		if err == nil && !resp.HasError() {
			return resp, nil
		}

		if err == nil && resp.HasError() {
			if !resp.IsRetryableError() {
				return resp, nil
			}
			lastResp = resp
			lastErr = fmt.Errorf("RPC error: %s", resp.Error.Message)
		} else if err != nil {
			lastErr = err
		}

		if c.ctx.Err() != nil {
			return nil, c.ctx.Err()
		}

		c.logger.Debug().
			Int("attempt", attempt+1).
			Int("maxAttempts", maxAttempts).
			Err(lastErr).
			Str("method", req.Method).
			Msg("plugin upstream call failed, retrying")
	}

	if lastResp != nil && lastResp.HasError() {
		return lastResp, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all upstreams failed: %v", lastErr)
	}

	return nil, fmt.Errorf("all upstreams failed")
}

// executeBatchWithRetry executes a batch with retry logic
func (c *PoolCaller) executeBatchWithRetry(requests []*jsonrpc.Request) ([]*jsonrpc.Response, error) {
	b := c.pool.GetSelector()

	methods := make([]string, 0, len(requests))
	seen := make(map[string]bool)
	for _, req := range requests {
		if !seen[req.Method] {
			seen[req.Method] = true
			methods = append(methods, req.Method)
		}
	}
	blockedExclude := c.pool.BuildBlockedMethodsExcludeForBatch(methods)

	if !c.retryConfig.Enabled {
		return c.executeBatchOnce(b, requests, blockedExclude)
	}

	tried := blockedExclude
	var lastErr error

	maxAttempts := c.retryConfig.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		responses, err := c.executeBatchOnce(b, requests, tried)

		if err == nil {
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
			lastErr = fmt.Errorf("batch has retryable errors")
		} else {
			lastErr = err
		}

		if c.ctx.Err() != nil {
			return nil, c.ctx.Err()
		}

		c.logger.Debug().
			Int("attempt", attempt+1).
			Int("maxAttempts", maxAttempts).
			Err(lastErr).
			Int("requests", len(requests)).
			Msg("plugin upstream batch call failed, retrying")
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all upstreams failed: %v", lastErr)
	}

	return nil, fmt.Errorf("all upstreams failed")
}

// executeOnce executes a single request on one upstream
func (c *PoolCaller) executeOnce(b balancer.Selector, req *jsonrpc.Request, exclude map[string]bool) (*jsonrpc.Response, error) {
	u := b.Next(exclude)
	if u == nil {
		return nil, fmt.Errorf("no upstreams available")
	}

	if exclude != nil {
		exclude[u.Name()] = true
	}

	return u.Execute(c.ctx, req)
}

// executeBatchOnce executes a batch on one upstream
func (c *PoolCaller) executeBatchOnce(b balancer.Selector, requests []*jsonrpc.Request, exclude map[string]bool) ([]*jsonrpc.Response, error) {
	u := b.Next(exclude)
	if u == nil {
		return nil, fmt.Errorf("no upstreams available")
	}

	if exclude != nil {
		exclude[u.Name()] = true
	}

	if !u.HasRPC() {
		return nil, fmt.Errorf("upstream %s does not support batch requests", u.Name())
	}

	return u.ExecuteBatch(c.ctx, requests)
}
