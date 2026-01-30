package proxy

import (
	"context"
	"io"
	"net/http"

	"github.com/rs/zerolog"

	"rpcgofer/internal/cache"
	"rpcgofer/internal/config"
	"rpcgofer/internal/jsonrpc"
	"rpcgofer/internal/upstream"
)

// Handler handles HTTP JSON-RPC requests
type Handler struct {
	router      *Router
	cache       cache.Cache
	retryConfig RetryConfig
	maxBodySize int64
	logger      zerolog.Logger
}

// NewHandler creates a new Handler
func NewHandler(router *Router, rpcCache cache.Cache, cfg *config.Config, logger zerolog.Logger) *Handler {
	return &Handler{
		router: router,
		cache:  rpcCache,
		retryConfig: RetryConfig{
			Enabled:     cfg.RetryEnabled,
			MaxAttempts: cfg.RetryMaxAttempts,
		},
		maxBodySize: cfg.MaxBodySize,
		logger:      logger.With().Str("component", "proxy").Logger(),
	}
}

// ServeHTTP handles HTTP requests
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get the pool from path
	pool, err := h.router.GetPoolFromPath(r.URL.Path)
	if err != nil {
		h.writeJSONRPCError(w, jsonrpc.NewIDNull(), jsonrpc.NewError(jsonrpc.CodeInvalidRequest, err.Error()))
		return
	}

	// Read request body
	var body []byte
	if h.maxBodySize > 0 {
		body, err = io.ReadAll(io.LimitReader(r.Body, h.maxBodySize+1))
		if err != nil {
			h.writeJSONRPCError(w, jsonrpc.NewIDNull(), jsonrpc.NewError(jsonrpc.CodeParseError, "failed to read request body"))
			return
		}
		if int64(len(body)) > h.maxBodySize {
			h.writeJSONRPCError(w, jsonrpc.NewIDNull(), jsonrpc.NewError(jsonrpc.CodeInvalidRequest, "request body too large"))
			return
		}
	} else {
		body, err = io.ReadAll(r.Body)
		if err != nil {
			h.writeJSONRPCError(w, jsonrpc.NewIDNull(), jsonrpc.NewError(jsonrpc.CodeParseError, "failed to read request body"))
			return
		}
	}

	// Parse JSON-RPC request(s)
	requests, isBatch, err := jsonrpc.ParseBatchRequest(body)
	if err != nil {
		h.writeJSONRPCError(w, jsonrpc.NewIDNull(), jsonrpc.ErrParse)
		return
	}

	// Validate requests
	for _, req := range requests {
		if err := req.Validate(); err != nil {
			h.writeJSONRPCError(w, req.ID, jsonrpc.NewError(jsonrpc.CodeInvalidRequest, err.Error()))
			return
		}
	}

	ctx := r.Context()
	groupName := extractGroupName(r.URL.Path)

	// Record method calls
	for _, req := range requests {
		pool.RecordMethod(req.Method)
	}

	// Execute request(s)
	if isBatch {
		responses := h.executeBatchWithCache(ctx, pool, groupName, requests)
		h.writeBatchResponse(w, responses)
	} else {
		req := requests[0]
		resp := h.executeWithCache(ctx, pool, groupName, req)
		h.writeResponse(w, resp)
	}
}

// executeWithCache executes a single request with caching support
func (h *Handler) executeWithCache(ctx context.Context, pool *upstream.Pool, groupName string, req *jsonrpc.Request) *jsonrpc.Response {
	// Check cache first
	if cache.IsCacheable(req.Method, req.Params) {
		cacheKey := cache.GenerateCacheKey(groupName, req.Method, req.Params)
		if cachedData, found := h.cache.Get(cacheKey); found {
			resp, err := jsonrpc.ParseResponse(cachedData)
			if err == nil {
				// Update response ID to match request ID
				resp.ID = req.ID
				h.logger.Debug().
					Str("method", req.Method).
					Str("cacheKey", cacheKey).
					Msg("cache hit")
				return resp
			}
		}
	}

	// Execute request
	resp, err := ExecuteWithPool(ctx, pool, req, h.retryConfig, h.logger)
	if err != nil {
		h.logger.Error().Err(err).Str("method", req.Method).Msg("request failed")
		return jsonrpc.NewErrorResponse(req.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "all upstreams failed"))
	}

	// Cache successful response
	if resp.IsSuccess() && cache.IsCacheable(req.Method, req.Params) {
		cacheKey := cache.GenerateCacheKey(groupName, req.Method, req.Params)
		if respBytes, err := resp.Bytes(); err == nil {
			h.cache.Set(cacheKey, respBytes)
			h.logger.Debug().
				Str("method", req.Method).
				Str("cacheKey", cacheKey).
				Msg("cached response")
		}
	}

	return resp
}

// executeBatchWithCache executes a batch of requests with caching support
func (h *Handler) executeBatchWithCache(ctx context.Context, pool *upstream.Pool, groupName string, requests []*jsonrpc.Request) []*jsonrpc.Response {
	responses := make([]*jsonrpc.Response, len(requests))
	uncachedIndices := make([]int, 0, len(requests))
	uncachedRequests := make([]*jsonrpc.Request, 0, len(requests))

	// Check cache for each request
	for i, req := range requests {
		if cache.IsCacheable(req.Method, req.Params) {
			cacheKey := cache.GenerateCacheKey(groupName, req.Method, req.Params)
			if cachedData, found := h.cache.Get(cacheKey); found {
				resp, err := jsonrpc.ParseResponse(cachedData)
				if err == nil {
					resp.ID = req.ID
					responses[i] = resp
					h.logger.Debug().
						Str("method", req.Method).
						Str("cacheKey", cacheKey).
						Msg("cache hit (batch)")
					continue
				}
			}
		}
		uncachedIndices = append(uncachedIndices, i)
		uncachedRequests = append(uncachedRequests, req)
	}

	// Execute uncached requests
	if len(uncachedRequests) > 0 {
		upstreamResponses, err := ExecuteBatchWithPool(ctx, pool, uncachedRequests, h.retryConfig, h.logger)
		if err != nil {
			h.logger.Error().Err(err).Msg("batch request failed")
			// Fill with errors for uncached requests
			for _, idx := range uncachedIndices {
				responses[idx] = jsonrpc.NewErrorResponse(requests[idx].ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "all upstreams failed"))
			}
		} else {
			// Map responses back and cache them
			for j, resp := range upstreamResponses {
				idx := uncachedIndices[j]
				responses[idx] = resp
				req := requests[idx]

				// Cache successful response
				if resp.IsSuccess() && cache.IsCacheable(req.Method, req.Params) {
					cacheKey := cache.GenerateCacheKey(groupName, req.Method, req.Params)
					if respBytes, err := resp.Bytes(); err == nil {
						h.cache.Set(cacheKey, respBytes)
						h.logger.Debug().
							Str("method", req.Method).
							Str("cacheKey", cacheKey).
							Msg("cached response (batch)")
					}
				}
			}
		}
	}

	return responses
}

// writeResponse writes a JSON-RPC response
func (h *Handler) writeResponse(w http.ResponseWriter, resp *jsonrpc.Response) {
	w.Header().Set("Content-Type", "application/json")
	data, err := resp.Bytes()
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to marshal response")
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Write(data)
}

// writeBatchResponse writes a batch of JSON-RPC responses
func (h *Handler) writeBatchResponse(w http.ResponseWriter, responses []*jsonrpc.Response) {
	w.Header().Set("Content-Type", "application/json")
	data, err := jsonrpc.MarshalBatchResponse(responses)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to marshal batch response")
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Write(data)
}

// writeJSONRPCError writes a JSON-RPC error response
func (h *Handler) writeJSONRPCError(w http.ResponseWriter, id jsonrpc.ID, rpcErr *jsonrpc.Error) {
	resp := jsonrpc.NewErrorResponse(id, rpcErr)
	h.writeResponse(w, resp)
}

// writeBatchError writes error responses for all requests in a batch
func (h *Handler) writeBatchError(w http.ResponseWriter, requests []*jsonrpc.Request, rpcErr *jsonrpc.Error) {
	responses := make([]*jsonrpc.Response, len(requests))
	for i, req := range requests {
		responses[i] = jsonrpc.NewErrorResponse(req.ID, rpcErr)
	}
	h.writeBatchResponse(w, responses)
}

// writeError writes a plain HTTP error
func (h *Handler) writeError(w http.ResponseWriter, status int, message string) {
	http.Error(w, message, status)
}
