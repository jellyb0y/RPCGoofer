package ws

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"rpcgofer/internal/batcher"
	"rpcgofer/internal/cache"
	"rpcgofer/internal/config"
	"rpcgofer/internal/jsonrpc"
	"rpcgofer/internal/plugin"
	"rpcgofer/internal/proxy"
	"rpcgofer/internal/subscription"
	"rpcgofer/internal/upstream"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 10 * 1024 * 1024 // 10MB
)

// Client represents a WebSocket client connection
type Client struct {
	conn            *websocket.Conn
	pool            *upstream.Pool
	groupName       string
	cache           cache.Cache
	subManager      *subscription.Manager
	pluginManager   *plugin.PluginManager
	batchAggregator *batcher.Aggregator
	retryConfig     proxy.RetryConfig
	logger          zerolog.Logger

	sendChan    chan []byte
	sendTimeout time.Duration
	closeChan   chan struct{}
	closeOnce sync.Once
}

// NewClient creates a new WebSocket client
func NewClient(conn *websocket.Conn, pool *upstream.Pool, groupName string, rpcCache cache.Cache, subManager *subscription.Manager, cfg *config.Config, pluginManager *plugin.PluginManager, batchAggregator *batcher.Aggregator, logger zerolog.Logger) *Client {
	return &Client{
		conn:            conn,
		pool:            pool,
		groupName:       groupName,
		cache:           rpcCache,
		subManager:      subManager,
		pluginManager:   pluginManager,
		batchAggregator: batchAggregator,
		retryConfig: proxy.RetryConfig{
			Enabled:     cfg.RetryEnabled,
			MaxAttempts: cfg.RetryMaxAttempts,
		},
		logger:       logger,
		sendChan:     make(chan []byte, 2048),
		sendTimeout:  cfg.GetWSSendTimeoutDuration(),
		closeChan:    make(chan struct{}),
	}
}

// Run starts the client read and write loops
func (c *Client) Run(ctx context.Context) {
	// Configure connection
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Start write goroutine
	go c.writePump(ctx)

	// Read loop (runs in current goroutine)
	c.readPump(ctx)
}

// readPump reads messages from the WebSocket connection
func (c *Client) readPump(ctx context.Context) {
	defer c.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closeChan:
			return
		default:
		}

		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.Debug().Err(err).Msg("read error")
			}
			return
		}

		// Process the message in a separate goroutine for parallel handling
		go c.handleMessage(ctx, data)
	}
}

// writePump writes messages to the WebSocket connection
func (c *Client) writePump(ctx context.Context) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closeChan:
			return
		case data := <-c.sendChan:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				c.logger.Debug().Err(err).Msg("write error")
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage processes an incoming message
func (c *Client) handleMessage(ctx context.Context, data []byte) {
	// Parse JSON-RPC request
	requests, isBatch, err := jsonrpc.ParseBatchRequest(data)
	if err != nil {
		c.sendError(jsonrpc.NewIDNull(), jsonrpc.ErrParse)
		return
	}

	// Record method calls
	for _, req := range requests {
		c.pool.RecordMethod(req.Method)
	}

	if isBatch {
		c.handleBatch(ctx, requests)
	} else {
		c.handleSingle(ctx, requests[0])
	}
}

// handleSingle handles a single JSON-RPC request
func (c *Client) handleSingle(ctx context.Context, req *jsonrpc.Request) {
	if err := req.Validate(); err != nil {
		c.sendError(req.ID, jsonrpc.NewError(jsonrpc.CodeInvalidRequest, err.Error()))
		return
	}

	// Check for subscription methods
	switch req.Method {
	case "eth_subscribe":
		c.handleSubscribe(ctx, req)
		return
	case "eth_unsubscribe":
		c.handleUnsubscribe(ctx, req)
		return
	}

	// Check if this method supports batching (before plugins and cache)
	if c.batchAggregator != nil && c.batchAggregator.GetConfig(req.Method) != nil {
		c.logger.Debug().
			Str("method", req.Method).
			Msg("adding to batch (ws)")
		responseChan := c.batchAggregator.Add(ctx, c.groupName, req)
		resp := <-responseChan
		c.sendResponse(resp)
		return
	}

	// Check if this method is handled by a plugin
	if c.pluginManager != nil && c.pluginManager.HasPlugin(req.Method) {
		caller := plugin.NewPoolCaller(ctx, c.pool, plugin.RetryConfig{Enabled: c.retryConfig.Enabled, MaxAttempts: c.retryConfig.MaxAttempts}, c.logger)
		c.logger.Debug().
			Str("method", req.Method).
			Msg("executing plugin (ws)")
		resp := c.pluginManager.Execute(ctx, req.Method, req.ID, req.Params, caller)
		c.sendResponse(resp)
		return
	}

	// Check cache first
	if cache.IsCacheable(req.Method, req.Params) {
		cacheKey := cache.GenerateCacheKey(c.groupName, req.Method, req.Params)
		if cachedData, found := c.cache.Get(cacheKey); found {
			resp, err := jsonrpc.ParseResponse(cachedData)
			if err == nil {
				resp.ID = req.ID
				c.logger.Debug().
					Str("method", req.Method).
					Str("cacheKey", cacheKey).
					Msg("cache hit (ws)")
				c.sendResponse(resp)
				return
			}
		}
	}

	// Forward to upstream via HTTP RPC (preferred)
	resp, err := proxy.ExecuteWithPool(ctx, c.pool, req, c.retryConfig, c.logger)
	if err != nil {
		c.sendError(req.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "all upstreams failed"))
		return
	}

	// Cache successful response (do not cache result: null)
	if resp.IsSuccess() && !resp.ResultIsNull() && cache.IsCacheable(req.Method, req.Params) {
		cacheKey := cache.GenerateCacheKey(c.groupName, req.Method, req.Params)
		if respBytes, err := resp.Bytes(); err == nil {
			c.cache.Set(cacheKey, respBytes)
			c.logger.Debug().
				Str("method", req.Method).
				Str("cacheKey", cacheKey).
				Msg("cached response (ws)")
		}
	}

	c.sendResponse(resp)
}

// handleBatch handles a batch of JSON-RPC requests
func (c *Client) handleBatch(ctx context.Context, requests []*jsonrpc.Request) {
	// Separate subscription requests from regular requests
	var regularReqs []*jsonrpc.Request
	var regularIndices []int
	var subscriptionReqs []*jsonrpc.Request

	for i, req := range requests {
		if req.Method == "eth_subscribe" || req.Method == "eth_unsubscribe" {
			subscriptionReqs = append(subscriptionReqs, req)
		} else {
			regularReqs = append(regularReqs, req)
			regularIndices = append(regularIndices, i)
		}
	}

	// Handle subscription requests individually
	for _, req := range subscriptionReqs {
		c.handleSingle(ctx, req)
	}

	// Handle regular requests as batch with caching
	if len(regularReqs) > 0 {
		// Validate all requests
		for _, req := range regularReqs {
			if err := req.Validate(); err != nil {
				c.sendError(req.ID, jsonrpc.NewError(jsonrpc.CodeInvalidRequest, err.Error()))
				return
			}
		}

		responses := make([]*jsonrpc.Response, len(regularReqs))
		uncachedIndices := make([]int, 0, len(regularReqs))
		uncachedRequests := make([]*jsonrpc.Request, 0, len(regularReqs))

		// Check batching, plugin and cache for each request
		for i, req := range regularReqs {
			// Check if this method supports batching
			if c.batchAggregator != nil && c.batchAggregator.GetConfig(req.Method) != nil {
				c.logger.Debug().
					Str("method", req.Method).
					Msg("adding to batch (ws batch)")
				responseChan := c.batchAggregator.Add(ctx, c.groupName, req)
				responses[i] = <-responseChan
				continue
			}

			// Check if this method is handled by a plugin
			if c.pluginManager != nil && c.pluginManager.HasPlugin(req.Method) {
				caller := plugin.NewPoolCaller(ctx, c.pool, plugin.RetryConfig{Enabled: c.retryConfig.Enabled, MaxAttempts: c.retryConfig.MaxAttempts}, c.logger)
				c.logger.Debug().
					Str("method", req.Method).
					Msg("executing plugin (ws batch)")
				responses[i] = c.pluginManager.Execute(ctx, req.Method, req.ID, req.Params, caller)
				continue
			}

			if cache.IsCacheable(req.Method, req.Params) {
				cacheKey := cache.GenerateCacheKey(c.groupName, req.Method, req.Params)
				if cachedData, found := c.cache.Get(cacheKey); found {
					resp, err := jsonrpc.ParseResponse(cachedData)
					if err == nil {
						resp.ID = req.ID
						responses[i] = resp
						c.logger.Debug().
							Str("method", req.Method).
							Str("cacheKey", cacheKey).
							Msg("cache hit (ws batch)")
						continue
					}
				}
			}
			uncachedIndices = append(uncachedIndices, i)
			uncachedRequests = append(uncachedRequests, req)
		}

		// Execute uncached requests
		if len(uncachedRequests) > 0 {
			upstreamResponses, err := proxy.ExecuteBatchWithPool(ctx, c.pool, uncachedRequests, c.retryConfig, c.logger)
			if err != nil {
				// Send error for each uncached request
				for _, idx := range uncachedIndices {
					responses[idx] = jsonrpc.NewErrorResponse(regularReqs[idx].ID, jsonrpc.NewError(jsonrpc.CodeInternalError, "all upstreams failed"))
				}
			} else {
				// Map responses back and cache them
				for j, resp := range upstreamResponses {
					idx := uncachedIndices[j]
					responses[idx] = resp
					req := regularReqs[idx]

					// Cache successful response (do not cache result: null)
					if resp.IsSuccess() && !resp.ResultIsNull() && cache.IsCacheable(req.Method, req.Params) {
						cacheKey := cache.GenerateCacheKey(c.groupName, req.Method, req.Params)
						if respBytes, err := resp.Bytes(); err == nil {
							c.cache.Set(cacheKey, respBytes)
							c.logger.Debug().
								Str("method", req.Method).
								Str("cacheKey", cacheKey).
								Msg("cached response (ws batch)")
						}
					}
				}
			}
		}

		c.sendBatchResponse(responses)
	}
}

// handleSubscribe handles eth_subscribe request
func (c *Client) handleSubscribe(ctx context.Context, req *jsonrpc.Request) {
	subType, params, err := req.GetSubscriptionType()
	if err != nil {
		c.sendError(req.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, err.Error()))
		return
	}

	subID, err := c.subManager.Subscribe(ctx, c.conn, c.send, c.pool, c.groupName, subType, params)
	if err != nil {
		c.sendError(req.ID, jsonrpc.NewError(jsonrpc.CodeInternalError, err.Error()))
		return
	}

	// Send subscription ID as result
	resp, _ := jsonrpc.NewResponse(req.ID, subID)
	c.sendResponse(resp)

	c.logger.Debug().
		Str("subID", subID).
		Str("type", subType).
		Msg("subscription created")
}

// handleUnsubscribe handles eth_unsubscribe request
func (c *Client) handleUnsubscribe(ctx context.Context, req *jsonrpc.Request) {
	subID, err := req.GetUnsubscribeID()
	if err != nil {
		c.sendError(req.ID, jsonrpc.NewError(jsonrpc.CodeInvalidParams, err.Error()))
		return
	}

	err = c.subManager.Unsubscribe(c.conn, subID)
	success := err == nil

	resp, _ := jsonrpc.NewResponse(req.ID, success)
	c.sendResponse(resp)

	c.logger.Debug().
		Str("subID", subID).
		Bool("success", success).
		Msg("unsubscribe requested")
}

// sendResponse sends a JSON-RPC response
func (c *Client) sendResponse(resp *jsonrpc.Response) {
	data, err := resp.Bytes()
	if err != nil {
		c.logger.Error().Err(err).Msg("failed to marshal response")
		return
	}
	c.send(data)
}

// sendBatchResponse sends a batch of JSON-RPC responses
func (c *Client) sendBatchResponse(responses []*jsonrpc.Response) {
	data, err := json.Marshal(responses)
	if err != nil {
		c.logger.Error().Err(err).Msg("failed to marshal batch response")
		return
	}
	c.send(data)
}

// sendError sends a JSON-RPC error response
func (c *Client) sendError(id jsonrpc.ID, rpcErr *jsonrpc.Error) {
	resp := jsonrpc.NewErrorResponse(id, rpcErr)
	c.sendResponse(resp)
}

// send sends data to the client
func (c *Client) send(data []byte) {
	select {
	case c.sendChan <- data:
	case <-c.closeChan:
	case <-time.After(c.sendTimeout):
		c.logger.Warn().Msg("send channel full, closing connection")
		c.Close()
	}
}

// Close closes the client connection
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closeChan)
		c.subManager.RemoveSession(c.conn)
		c.conn.Close()
		c.logger.Debug().Msg("client closed")
	})
}
