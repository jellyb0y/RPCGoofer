package batcher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
	"rpcgofer/internal/jsonrpc"
)

// BatchExecutor executes batched requests
type BatchExecutor interface {
	ExecuteBatch(ctx context.Context, groupName string, req *jsonrpc.Request) *jsonrpc.Response
}

// Aggregator aggregates requests for batching
type Aggregator struct {
	config   map[string]*config.BatchMethodConfig       // method -> config
	buckets  map[string]map[BatchKey]*BatchBucket       // groupName -> batchKey -> bucket
	executor BatchExecutor
	logger   zerolog.Logger
	mu       sync.RWMutex
}

// NewAggregator creates a new batch aggregator
func NewAggregator(cfg *config.BatchingConfig, logger zerolog.Logger) *Aggregator {
	methodConfigs := make(map[string]*config.BatchMethodConfig)
	if cfg != nil {
		for method, methodCfg := range cfg.Methods {
			cfgCopy := methodCfg
			methodConfigs[method] = &cfgCopy
		}
	}

	return &Aggregator{
		config:  methodConfigs,
		buckets: make(map[string]map[BatchKey]*BatchBucket),
		logger:  logger.With().Str("component", "batcher").Logger(),
	}
}

// SetExecutor sets the batch executor
func (a *Aggregator) SetExecutor(executor BatchExecutor) {
	a.executor = executor
}

// GetConfig returns the config for a method, or nil if batching is not enabled
func (a *Aggregator) GetConfig(method string) *config.BatchMethodConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config[method]
}

// Add adds a request to the batch and returns a channel for the response
func (a *Aggregator) Add(ctx context.Context, groupName string, req *jsonrpc.Request) <-chan *jsonrpc.Response {
	responseChan := make(chan *jsonrpc.Response, 1)

	cfg := a.GetConfig(req.Method)
	if cfg == nil {
		// Method doesn't support batching
		responseChan <- jsonrpc.NewErrorResponse(req.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "batching not configured for method"))
		return responseChan
	}

	// Parse params
	var params []interface{}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		responseChan <- jsonrpc.NewErrorResponse(req.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "failed to parse params"))
		return responseChan
	}

	var elements []interface{}
	var keyParams []interface{}

	if cfg.Spread {
		// Spread mode: entire params array is the elements to aggregate
		// No key params in this mode
		elements = params
		keyParams = nil
	} else {
		// Standard mode: aggregate specific param index
		// Validate aggregate param index
		if cfg.AggregateParam >= len(params) {
			responseChan <- jsonrpc.NewErrorResponse(req.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "aggregate param index out of range"))
			return responseChan
		}

		// Extract aggregate elements
		aggregateValue := params[cfg.AggregateParam]
		var ok bool
		elements, ok = toSlice(aggregateValue)
		if !ok {
			// Single element - wrap in slice
			elements = []interface{}{aggregateValue}
		}

		// Build key params (all params except aggregate)
		keyParams = make([]interface{}, 0, len(params)-1)
		for i, p := range params {
			if i != cfg.AggregateParam {
				keyParams = append(keyParams, p)
			}
		}
	}

	if len(elements) == 0 {
		// Empty array - return empty result immediately
		emptyResult, _ := json.Marshal([]interface{}{})
		responseChan <- &jsonrpc.Response{
			JSONRPC: jsonrpc.Version,
			ID:      req.ID,
			Result:  emptyResult,
		}
		return responseChan
	}

	// Generate batch key
	batchKey := a.generateBatchKey(req.Method, keyParams)

	// Create batch item
	item := &BatchItem{
		ID:           req.ID,
		Elements:     elements,
		ElementCount: len(elements),
		ResponseChan: responseChan,
	}

	// Add to bucket
	a.mu.Lock()

	// Ensure group map exists
	if a.buckets[groupName] == nil {
		a.buckets[groupName] = make(map[BatchKey]*BatchBucket)
	}

	// Get or create bucket
	bucket := a.buckets[groupName][batchKey]
	if bucket == nil || bucket.IsEmpty() {
		bucket = NewBatchBucket(req.Method, keyParams, cfg)
		a.buckets[groupName][batchKey] = bucket
	}

	shouldFlush := bucket.Add(item)

	// Start timer for first item
	bucket.StartTimer(func() {
		a.flush(ctx, groupName, batchKey)
	})

	a.mu.Unlock()

	// Flush if max size reached
	if shouldFlush {
		go a.flush(ctx, groupName, batchKey)
	}

	return responseChan
}

// generateBatchKey generates a unique key for the batch
func (a *Aggregator) generateBatchKey(method string, keyParams []interface{}) BatchKey {
	if len(keyParams) == 0 {
		return BatchKey(method + ":")
	}
	keyJSON, _ := json.Marshal(keyParams)
	return BatchKey(method + ":" + string(keyJSON))
}

// flush executes the batch and distributes results
func (a *Aggregator) flush(ctx context.Context, groupName string, batchKey BatchKey) {
	a.mu.Lock()
	groupBuckets := a.buckets[groupName]
	if groupBuckets == nil {
		a.mu.Unlock()
		return
	}

	bucket := groupBuckets[batchKey]
	if bucket == nil {
		a.mu.Unlock()
		return
	}

	if bucket.IsEmpty() {
		a.mu.Unlock()
		return
	}

	newBucket := NewBatchBucket(bucket.GetMethod(), bucket.GetKeyParams(), bucket.GetConfig())
	groupBuckets[batchKey] = newBucket
	a.mu.Unlock()

	items, totalCount := bucket.TakeItems()
	if items == nil || len(items) == 0 {
		return
	}

	// Aggregate all elements
	aggregated := make([]interface{}, 0, totalCount)
	for _, item := range items {
		aggregated = append(aggregated, item.Elements...)
	}

	// Build aggregated request params
	cfg := bucket.GetConfig()
	keyParams := bucket.GetKeyParams()

	var params []interface{}
	if cfg.Spread {
		// Spread mode: params IS the aggregated array directly
		params = aggregated
	} else {
		// Standard mode: insert aggregated array at aggregateParam index
		params = buildParams(keyParams, aggregated, cfg.AggregateParam)
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		a.logger.Error().Err(err).Msg("failed to marshal batch params")
		errResp := jsonrpc.NewError(jsonrpc.CodeInternalError, "failed to build batch request")
		for _, item := range items {
			item.ResponseChan <- jsonrpc.NewErrorResponse(item.ID, errResp)
		}
		return
	}

	// Build request
	batchReq := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      jsonrpc.NewIDInt(1),
		Method:  bucket.GetMethod(),
		Params:  paramsJSON,
	}

	a.logger.Debug().
		Str("method", bucket.GetMethod()).
		Int("items", len(items)).
		Int("elements", totalCount).
		Msg("executing batch")

	// Execute batch
	if a.executor == nil {
		errResp := jsonrpc.NewError(jsonrpc.CodeInternalError, "batch executor not set")
		for _, item := range items {
			item.ResponseChan <- jsonrpc.NewErrorResponse(item.ID, errResp)
		}
		return
	}

	resp := a.executor.ExecuteBatch(ctx, groupName, batchReq)

	// Handle error response
	if resp.HasError() {
		for _, item := range items {
			item.ResponseChan <- jsonrpc.NewErrorResponse(item.ID, resp.Error)
		}
		return
	}

	// Parse result as array
	var results []interface{}
	if err := json.Unmarshal(resp.Result, &results); err != nil {
		a.logger.Error().
			Err(err).
			Str("method", bucket.GetMethod()).
			Msg("batch result is not an array")

		errResp := jsonrpc.NewError(-32002, "batch result is not an array")
		for _, item := range items {
			item.ResponseChan <- jsonrpc.NewErrorResponse(item.ID, errResp)
		}
		return
	}

	// Validate result size
	if len(results) != totalCount {
		a.logger.Error().
			Str("method", bucket.GetMethod()).
			Int("expected", totalCount).
			Int("got", len(results)).
			Msg("batch result size mismatch")

		errResp := jsonrpc.NewError(-32002, fmt.Sprintf("batch result size mismatch: expected %d, got %d", totalCount, len(results)))
		for _, item := range items {
			item.ResponseChan <- jsonrpc.NewErrorResponse(item.ID, errResp)
		}
		return
	}

	// Distribute results to each client
	offset := 0
	for _, item := range items {
		itemResults := results[offset : offset+item.ElementCount]
		offset += item.ElementCount

		resultJSON, err := json.Marshal(itemResults)
		if err != nil {
			item.ResponseChan <- jsonrpc.NewErrorResponse(item.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "failed to marshal result"))
			continue
		}

		item.ResponseChan <- &jsonrpc.Response{
			JSONRPC: jsonrpc.Version,
			ID:      item.ID,
			Result:  resultJSON,
		}
	}

	a.logger.Debug().
		Str("method", bucket.GetMethod()).
		Int("items", len(items)).
		Msg("batch completed")
}

// FlushAll flushes all pending batches (for graceful shutdown)
func (a *Aggregator) FlushAll(ctx context.Context) {
	a.mu.Lock()
	keysToFlush := make(map[string][]BatchKey)
	for groupName, groupBuckets := range a.buckets {
		for key := range groupBuckets {
			keysToFlush[groupName] = append(keysToFlush[groupName], key)
		}
	}
	a.mu.Unlock()

	for groupName, keys := range keysToFlush {
		for _, key := range keys {
			a.flush(ctx, groupName, key)
		}
	}
}

// Close stops all timers and flushes pending batches
func (a *Aggregator) Close(ctx context.Context) {
	a.mu.Lock()
	for _, groupBuckets := range a.buckets {
		for _, bucket := range groupBuckets {
			bucket.StopTimer()
		}
	}
	a.mu.Unlock()

	a.FlushAll(ctx)
	a.logger.Info().Msg("batch aggregator closed")
}

// GetMethods returns list of methods with batching enabled
func (a *Aggregator) GetMethods() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	methods := make([]string, 0, len(a.config))
	for method := range a.config {
		methods = append(methods, method)
	}
	return methods
}

// buildParams builds the full params array with aggregated elements
func buildParams(keyParams []interface{}, aggregated []interface{}, aggregateIndex int) []interface{} {
	totalLen := len(keyParams) + 1
	result := make([]interface{}, totalLen)

	keyIdx := 0
	for i := 0; i < totalLen; i++ {
		if i == aggregateIndex {
			result[i] = aggregated
		} else {
			if keyIdx < len(keyParams) {
				result[i] = keyParams[keyIdx]
				keyIdx++
			}
		}
	}

	return result
}

// toSlice converts an interface{} to []interface{} if possible
func toSlice(v interface{}) ([]interface{}, bool) {
	switch val := v.(type) {
	case []interface{}:
		return val, true
	case []string:
		result := make([]interface{}, len(val))
		for i, s := range val {
			result[i] = s
		}
		return result, true
	default:
		return nil, false
	}
}
