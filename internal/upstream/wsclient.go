package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"rpcgofer/internal/jsonrpc"
)

type subscriptionHandler func(result json.RawMessage)

type subParamsEntry struct {
	subType string
	params  json.RawMessage
	handler subscriptionHandler
}

// UpstreamWSClient owns a single WebSocket connection for an upstream.
// It multiplexes RPC request/response and eth_subscribe events on one connection.
type UpstreamWSClient struct {
	wsURL             string
	messageTimeout    time.Duration
	reconnectInterval time.Duration
	pingInterval      time.Duration
	upstream          *Upstream
	logger            zerolog.Logger

	conn   *websocket.Conn
	connMu sync.RWMutex
	writeMu sync.Mutex

	pending    map[int64]chan *jsonrpc.Response
	pendingMu  sync.Mutex
	reqID      int64
	subHandlers map[string]subscriptionHandler
	subParams   map[string]subParamsEntry
	subMu      sync.Mutex

	// firstEventAfterConnect: 0 = not yet logged first subscription event for this connection, 1 = already logged
	firstEventAfterConnect uint32

	msgCount    int64
	lastReadAt  time.Time
	readCountMu sync.Mutex

	eventChan chan []byte

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewUpstreamWSClient creates a new WebSocket client for an upstream
func NewUpstreamWSClient(wsURL string, messageTimeout time.Duration, reconnectInterval time.Duration, pingInterval time.Duration, u *Upstream, logger zerolog.Logger) *UpstreamWSClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &UpstreamWSClient{
		wsURL:             wsURL,
		messageTimeout:    messageTimeout,
		reconnectInterval: reconnectInterval,
		pingInterval:      pingInterval,
		upstream:          u,
		logger:            logger,
		pending:           make(map[int64]chan *jsonrpc.Response),
		subHandlers:       make(map[string]subscriptionHandler),
		subParams:         make(map[string]subParamsEntry),
		eventChan:         make(chan []byte, 1024),
		ctx:               ctx,
		cancel:            cancel,
	}
}

// Connect establishes the WebSocket connection and starts the reader goroutine
func (c *UpstreamWSClient) Connect(ctx context.Context) error {
	c.connMu.Lock()
	if c.conn != nil {
		c.connMu.Unlock()
		return nil
	}
	c.connMu.Unlock()

	c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket connecting")
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect WebSocket: %w", err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	c.setPongHandler(conn)
	atomic.StoreUint32(&c.firstEventAfterConnect, 0)
	c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket connected")
	c.wg.Add(1)
	go c.dispatchWorker()
	c.wg.Add(1)
	go c.readLoop()
	if c.pingInterval > 0 {
		c.wg.Add(1)
		go c.pingLoop()
	}
	return nil
}

func (c *UpstreamWSClient) setPongHandler(conn *websocket.Conn) {
	readTimeout := c.messageTimeout
	if readTimeout == 0 {
		readTimeout = 60 * time.Second
	}
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})
}

func (c *UpstreamWSClient) pingLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.connMu.RLock()
			conn := c.conn
			c.connMu.RUnlock()
			if conn == nil {
				return
			}
			c.writeMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
			c.writeMu.Unlock()
			if err != nil {
				c.logger.Debug().Str("upstream", c.upstream.Name()).Err(err).Msg("ping write failed")
				return
			}
		}
	}
}

// Connected returns true if the WebSocket connection is established
func (c *UpstreamWSClient) Connected() bool {
	c.connMu.RLock()
	ok := c.conn != nil
	c.connMu.RUnlock()
	return ok
}

// Close closes the connection and stops the reader
func (c *UpstreamWSClient) Close() {
	c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket closing")
	c.cancel()
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()

	c.pendingMu.Lock()
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = make(map[int64]chan *jsonrpc.Response)
	c.pendingMu.Unlock()

	close(c.eventChan)
	c.wg.Wait()
	c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket disconnected")
}

// SendRequest sends an RPC request and waits for the response
func (c *UpstreamWSClient) SendRequest(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Response, error) {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return nil, fmt.Errorf("WebSocket not connected")
	}

	reqID := atomic.AddInt64(&c.reqID, 1)
	respChan := make(chan *jsonrpc.Response, 1)

	c.pendingMu.Lock()
	c.pending[reqID] = respChan
	c.pendingMu.Unlock()

	wsReq := req.Clone()
	wsReq.ID = jsonrpc.NewIDInt(reqID)

	reqBytes, err := wsReq.Bytes()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	c.connMu.RLock()
	conn = c.conn
	c.connMu.RUnlock()

	if conn == nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("WebSocket not connected")
	}

	c.writeMu.Lock()
	writeErr := conn.WriteMessage(websocket.TextMessage, reqBytes)
	c.writeMu.Unlock()
	if writeErr != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("failed to send request: %w", writeErr)
	}

	c.upstream.IncrementRequestCount()

	select {
	case resp := <-respChan:
		if resp != nil {
			resp.ID = req.ID
			return resp, nil
		}
		return nil, fmt.Errorf("connection closed")
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return nil, ctx.Err()
	}
}

// Subscribe sends eth_subscribe and registers a handler for subscription events
func (c *UpstreamWSClient) Subscribe(ctx context.Context, subType string, params json.RawMessage, onEvent subscriptionHandler) (string, error) {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return "", fmt.Errorf("WebSocket not connected")
	}

	var subParams []interface{}
	subParams = append(subParams, subType)
	if params != nil && len(params) > 0 && string(params) != "null" {
		var additionalParams interface{}
		if err := json.Unmarshal(params, &additionalParams); err == nil {
			subParams = append(subParams, additionalParams)
		}
	}

	reqID := atomic.AddInt64(&c.reqID, 1)
	respChan := make(chan *jsonrpc.Response, 1)

	c.pendingMu.Lock()
	c.pending[reqID] = respChan
	c.pendingMu.Unlock()

	req, err := jsonrpc.NewRequest("eth_subscribe", subParams, jsonrpc.NewIDInt(reqID))
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", fmt.Errorf("failed to create subscribe request: %w", err)
	}

	reqBytes, err := req.Bytes()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	c.connMu.RLock()
	conn = c.conn
	c.connMu.RUnlock()

	if conn == nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", fmt.Errorf("WebSocket not connected")
	}

	c.writeMu.Lock()
	writeErr := conn.WriteMessage(websocket.TextMessage, reqBytes)
	c.writeMu.Unlock()
	if writeErr != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", fmt.Errorf("failed to send subscribe request: %w", writeErr)
	}

	var upstreamSubID string
	select {
	case resp := <-respChan:
		if resp == nil {
			return "", fmt.Errorf("connection closed")
		}
		if resp.HasError() {
			return "", fmt.Errorf("subscription error: %s", resp.Error.Message)
		}
		if err := json.Unmarshal(resp.Result, &upstreamSubID); err != nil {
			return "", fmt.Errorf("failed to parse subscription ID: %w", err)
		}
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", ctx.Err()
	}

	c.subMu.Lock()
	c.subHandlers[upstreamSubID] = onEvent
	c.subParams[upstreamSubID] = subParamsEntry{subType: subType, params: params, handler: onEvent}
	c.subMu.Unlock()

	c.upstream.IncrementSubscriptionCount()
	return upstreamSubID, nil
}

func (c *UpstreamWSClient) subscribeInternal(ctx context.Context, subType string, params json.RawMessage, onEvent subscriptionHandler) (string, error) {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return "", fmt.Errorf("WebSocket not connected")
	}

	var subParams []interface{}
	subParams = append(subParams, subType)
	if params != nil && len(params) > 0 && string(params) != "null" {
		var additionalParams interface{}
		if err := json.Unmarshal(params, &additionalParams); err == nil {
			subParams = append(subParams, additionalParams)
		}
	}

	reqID := atomic.AddInt64(&c.reqID, 1)
	respChan := make(chan *jsonrpc.Response, 1)

	c.pendingMu.Lock()
	c.pending[reqID] = respChan
	c.pendingMu.Unlock()

	req, err := jsonrpc.NewRequest("eth_subscribe", subParams, jsonrpc.NewIDInt(reqID))
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", fmt.Errorf("failed to create subscribe request: %w", err)
	}

	reqBytes, err := req.Bytes()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	c.connMu.RLock()
	conn = c.conn
	c.connMu.RUnlock()

	if conn == nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", fmt.Errorf("WebSocket not connected")
	}

	c.writeMu.Lock()
	writeErr := conn.WriteMessage(websocket.TextMessage, reqBytes)
	c.writeMu.Unlock()
	if writeErr != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", fmt.Errorf("failed to send subscribe request: %w", writeErr)
	}

	var upstreamSubID string
	select {
	case resp := <-respChan:
		if resp == nil {
			return "", fmt.Errorf("connection closed")
		}
		if resp.HasError() {
			return "", fmt.Errorf("subscription error: %s", resp.Error.Message)
		}
		if err := json.Unmarshal(resp.Result, &upstreamSubID); err != nil {
			return "", fmt.Errorf("failed to parse subscription ID: %w", err)
		}
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return "", ctx.Err()
	}

	return upstreamSubID, nil
}

// Unsubscribe sends eth_unsubscribe and removes the handler
func (c *UpstreamWSClient) Unsubscribe(subID string) {
	c.subMu.Lock()
	delete(c.subHandlers, subID)
	delete(c.subParams, subID)
	c.subMu.Unlock()

	c.upstream.DecrementSubscriptionCount()

	reqID := atomic.AddInt64(&c.reqID, 1)
	req, err := jsonrpc.NewRequest("eth_unsubscribe", []string{subID}, jsonrpc.NewIDInt(reqID))
	if err != nil {
		return
	}

	reqBytes, err := req.Bytes()
	if err != nil {
		return
	}

	c.writeMu.Lock()
	c.connMu.RLock()
	conn := c.conn
	if conn == nil {
		c.connMu.RUnlock()
		c.writeMu.Unlock()
		return
	}
	_ = conn.WriteMessage(websocket.TextMessage, reqBytes)
	c.connMu.RUnlock()
	c.writeMu.Unlock()
}

func (c *UpstreamWSClient) readLoop() {
	defer c.wg.Done()

	readTimeout := c.messageTimeout
	if readTimeout == 0 {
		readTimeout = 60 * time.Second
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		c.connMu.RLock()
		conn := c.conn
		c.connMu.RUnlock()

		if conn == nil {
			c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket reader stopped (no connection)")
			return
		}

		c.readCountMu.Lock()
		lastRead := c.lastReadAt
		msgCount := c.msgCount
		c.readCountMu.Unlock()
		c.logger.Debug().
			Str("upstream", c.upstream.Name()).
			Int64("msgCount", msgCount).
			Msg("readLoop entering ReadMessage")

		conn.SetReadDeadline(time.Now().Add(readTimeout))
		_, data, err := conn.ReadMessage()
		if err != nil {
			select {
			case <-c.ctx.Done():
				c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket reader stopped (shutdown)")
				return
			default:
			}

			c.readCountMu.Lock()
			lastRead = c.lastReadAt
			c.readCountMu.Unlock()
			c.logger.Warn().
				Str("upstream", c.upstream.Name()).
				Err(err).
				Time("lastReadAt", lastRead).
				Msg("WebSocket connection lost, reconnecting")
			if c.reconnect() {
				continue
			}
			c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket reader stopped (shutdown)")
			return
		}

		c.readCountMu.Lock()
		c.msgCount++
		c.lastReadAt = time.Now()
		c.readCountMu.Unlock()

		if c.isSubscriptionMessage(data) {
			// WIP debug, remove after fix
			if subID := c.parseSubscriptionID(data); subID != "" {
				c.subMu.Lock()
				entry, ok := c.subParams[subID]
				c.subMu.Unlock()
				if ok && entry.subType == "newHeads" {
					c.logger.Info().
						Str("upstream", c.upstream.Name()).
						Str("subscription", subID).
						Msg("newHeads: block received from upstream")
				}
			}
			select {
			case <-c.ctx.Done():
				return
			case c.eventChan <- data:
			default:
				c.logger.Warn().Str("upstream", c.upstream.Name()).Msg("event queue full, dropping subscription message")
			}
		} else {
			c.dispatchMessage(data)
		}
	}
}

func (c *UpstreamWSClient) isSubscriptionMessage(data []byte) bool {
	var base struct {
		Method string          `json:"method"`
		Params *struct {
			Subscription string `json:"subscription"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return false
	}
	return base.Method == "eth_subscription" && base.Params != nil
}

func (c *UpstreamWSClient) parseSubscriptionID(data []byte) string {
	var base struct {
		Params *struct {
			Subscription string `json:"subscription"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &base); err != nil || base.Params == nil {
		return ""
	}
	return base.Params.Subscription
}

func (c *UpstreamWSClient) dispatchWorker() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case data, ok := <-c.eventChan:
			if !ok {
				return
			}
			c.dispatchMessage(data)
		}
	}
}

func (c *UpstreamWSClient) dispatchMessage(data []byte) {
	var base struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Params *struct {
			Subscription string          `json:"subscription"`
			Result       json.RawMessage `json:"result"`
		} `json:"params"`
	}

	if err := json.Unmarshal(data, &base); err != nil {
		c.logger.Warn().
			Str("upstream", c.upstream.Name()).
			Err(err).
			Int("len", len(data)).
			Msg("ws message parse error")
		return
	}

	if base.Method == "eth_subscription" && base.Params != nil {
		c.subMu.Lock()
		handler, exists := c.subHandlers[base.Params.Subscription]
		c.subMu.Unlock()

		if !exists || handler == nil {
			c.logger.Warn().
				Str("upstream", c.upstream.Name()).
				Str("subscription", base.Params.Subscription).
				Msg("subscription notification, no handler")
			return
		}

		if atomic.CompareAndSwapUint32(&c.firstEventAfterConnect, 0, 1) {
			c.logger.Debug().
				Str("upstream", c.upstream.Name()).
				Str("subscription", base.Params.Subscription).
				Msg("first subscription event after connect")
		}

		c.upstream.IncrementSubscriptionEvents()
		result := base.Params.Result
		go func() {
			defer func() {
				if r := recover(); r != nil {
					c.logger.Error().Interface("panic", r).Str("upstream", c.upstream.Name()).Msg("subscription handler panic")
				}
			}()
			start := time.Now()
			handler(result)
			if d := time.Since(start); d > 2*time.Second {
				c.logger.Warn().
					Str("upstream", c.upstream.Name()).
					Dur("handlerDuration", d).
					Msg("subscription handler slow")
			}
		}()
		return
	}

	if len(base.ID) > 0 && string(base.ID) != "null" {
		var idVal interface{}
		if err := json.Unmarshal(base.ID, &idVal); err != nil {
			return
		}
		var reqID int64
		switch v := idVal.(type) {
		case float64:
			reqID = int64(v)
		case int64:
			reqID = v
		default:
			return
		}

		var resp jsonrpc.Response
		if err := json.Unmarshal(data, &resp); err != nil {
			return
		}

		c.pendingMu.Lock()
		ch, exists := c.pending[reqID]
		if exists {
			delete(c.pending, reqID)
		}
		c.pendingMu.Unlock()

		if exists && ch != nil {
			select {
			case ch <- &resp:
			default:
			}
		}
	}
}

func (c *UpstreamWSClient) reconnect() bool {
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket connection closed, starting reconnection loop")

	c.pendingMu.Lock()
	for _, ch := range c.pending {
		select {
		case ch <- nil:
		default:
		}
	}
	c.pending = make(map[int64]chan *jsonrpc.Response)
	c.pendingMu.Unlock()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	interval := c.reconnectInterval
	if interval < 3*time.Second {
		interval = 3 * time.Second
	}
	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket reconnection stopped (shutdown)")
			return false
		case <-time.After(interval):
		}

		c.logger.Info().Str("upstream", c.upstream.Name()).Dur("interval", interval).Msg("WebSocket reconnection attempt")

		ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
		conn, _, err := dialer.DialContext(ctx, c.wsURL, nil)
		cancel()
		if err != nil {
			c.logger.Warn().Str("upstream", c.upstream.Name()).Err(err).Dur("nextRetry", interval).Msg("WebSocket reconnection failed, will retry")
			continue
		}

		c.connMu.Lock()
		c.conn = conn
		c.connMu.Unlock()

		c.setPongHandler(conn)
		atomic.StoreUint32(&c.firstEventAfterConnect, 0)
		c.logger.Info().Str("upstream", c.upstream.Name()).Msg("WebSocket reconnected successfully")

		c.subMu.Lock()
		toResubscribe := make([]subParamsEntry, 0, len(c.subParams))
		for _, entry := range c.subParams {
			toResubscribe = append(toResubscribe, entry)
		}
		c.subHandlers = make(map[string]subscriptionHandler)
		c.subParams = make(map[string]subParamsEntry)
		c.subMu.Unlock()

		go func(entries []subParamsEntry) {
			total := len(entries)
			var okCount, failCount int
			for _, entry := range entries {
				resubCtx, resubCancel := context.WithTimeout(c.ctx, 10*time.Second)
				subID, err := c.subscribeInternal(resubCtx, entry.subType, entry.params, entry.handler)
				resubCancel()

				if err != nil {
					failCount++
					c.logger.Warn().Err(err).Str("subType", entry.subType).Msg("failed to re-subscribe")
					continue
				}
				okCount++
				c.logger.Info().
					Str("upstream", c.upstream.Name()).
					Str("subType", entry.subType).
					Str("subID", subID).
					Msg("re-subscribed")

				c.subMu.Lock()
				c.subParams[subID] = entry
				c.subHandlers[subID] = entry.handler
				c.subMu.Unlock()
			}
			c.logger.Info().
				Str("upstream", c.upstream.Name()).
				Int("total", total).
				Int("ok", okCount).
				Int("failed", failCount).
				Msg("reconnect resubscribe done")
		}(toResubscribe)

		return true
	}
}
