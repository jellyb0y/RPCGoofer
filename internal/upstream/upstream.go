package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
	"rpcgofer/internal/jsonrpc"
)

// Upstream represents a single upstream RPC endpoint
type Upstream struct {
	name   string
	rpcURL string
	wsURL  string
	weight int
	role   Role

	httpClient *http.Client
	status     *Status
	logger     zerolog.Logger

	// WebSocket connection for subscriptions
	wsMu     sync.Mutex
	wsConn   *websocket.Conn
	wsReqID  int64
	wsPending map[int64]chan *jsonrpc.Response
}

// Config for creating a new Upstream
type Config struct {
	Name           string
	RPCURL         string
	WSURL          string
	Weight         int
	Role           Role
	RequestTimeout time.Duration
	Logger         zerolog.Logger
}

// NewUpstream creates a new Upstream instance
func NewUpstream(cfg Config) *Upstream {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   cfg.RequestTimeout,
	}

	return &Upstream{
		name:       cfg.Name,
		rpcURL:     cfg.RPCURL,
		wsURL:      cfg.WSURL,
		weight:     cfg.Weight,
		role:       cfg.Role,
		httpClient: httpClient,
		status:     NewStatus(),
		logger:     cfg.Logger.With().Str("upstream", cfg.Name).Logger(),
		wsPending:  make(map[int64]chan *jsonrpc.Response),
	}
}

// NewUpstreamFromConfig creates an Upstream from config
func NewUpstreamFromConfig(cfg config.UpstreamConfig, globalCfg *config.Config, logger zerolog.Logger) *Upstream {
	return NewUpstream(Config{
		Name:           cfg.Name,
		RPCURL:         cfg.RPCURL,
		WSURL:          cfg.WSURL,
		Weight:         cfg.Weight,
		Role:           RoleFromConfig(cfg.Role),
		RequestTimeout: globalCfg.GetRequestTimeoutDuration(),
		Logger:         logger,
	})
}

// Name returns the upstream name
func (u *Upstream) Name() string {
	return u.name
}

// RPCURL returns the HTTP RPC URL
func (u *Upstream) RPCURL() string {
	return u.rpcURL
}

// WSURL returns the WebSocket URL
func (u *Upstream) WSURL() string {
	return u.wsURL
}

// Weight returns the weight for load balancing
func (u *Upstream) Weight() int {
	return u.weight
}

// Role returns the upstream role
func (u *Upstream) Role() Role {
	return u.role
}

// IsMain returns true if this is a main upstream
func (u *Upstream) IsMain() bool {
	return u.role == RoleMain
}

// IsFallback returns true if this is a fallback upstream
func (u *Upstream) IsFallback() bool {
	return u.role == RoleFallback
}

// IsHealthy returns the health status
func (u *Upstream) IsHealthy() bool {
	return u.status.IsHealthy()
}

// SetHealthy sets the health status
func (u *Upstream) SetHealthy(healthy bool) {
	u.status.SetHealthy(healthy)
}

// GetCurrentBlock returns the current block number
func (u *Upstream) GetCurrentBlock() uint64 {
	return u.status.GetCurrentBlock()
}

// SetCurrentBlock sets the current block number
func (u *Upstream) SetCurrentBlock(block uint64) {
	u.status.SetCurrentBlock(block)
}

// UpdateBlock updates the block if the new value is higher
func (u *Upstream) UpdateBlock(block uint64) bool {
	return u.status.UpdateBlock(block)
}

// IncrementRequestCount increments the request counter
func (u *Upstream) IncrementRequestCount() {
	u.status.IncrementRequestCount()
}

// IncrementRequestCountBy increments the request counter by a given value
func (u *Upstream) IncrementRequestCountBy(count uint64) {
	u.status.IncrementRequestCountBy(count)
}

// SwapRequestCount returns the current request count and resets it to zero
func (u *Upstream) SwapRequestCount() uint64 {
	return u.status.SwapRequestCount()
}

// IncrementSubscriptionCount increments the subscription counter
func (u *Upstream) IncrementSubscriptionCount() {
	u.status.IncrementSubscriptionCount()
}

// DecrementSubscriptionCount decrements the subscription counter
func (u *Upstream) DecrementSubscriptionCount() {
	u.status.DecrementSubscriptionCount()
}

// GetSubscriptionCount returns the current subscription count
func (u *Upstream) GetSubscriptionCount() int64 {
	return u.status.GetSubscriptionCount()
}

// IncrementSubscriptionEvents increments the subscription events counter
func (u *Upstream) IncrementSubscriptionEvents() {
	u.status.IncrementSubscriptionEvents()
}

// SwapSubscriptionEvents returns the current subscription events count and resets it to zero
func (u *Upstream) SwapSubscriptionEvents() uint64 {
	return u.status.SwapSubscriptionEvents()
}

// GetLastBlockTime returns the time of the last block update
func (u *Upstream) GetLastBlockTime() time.Time {
	return u.status.GetLastBlockTime()
}

// HasRPC returns true if HTTP RPC URL is configured
func (u *Upstream) HasRPC() bool {
	return u.rpcURL != ""
}

// HasWS returns true if WebSocket URL is configured
func (u *Upstream) HasWS() bool {
	return u.wsURL != ""
}

// Execute sends a JSON-RPC request and returns the response
// It prefers HTTP RPC, falls back to WebSocket if HTTP is not available
func (u *Upstream) Execute(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Response, error) {
	if u.HasRPC() {
		return u.ExecuteHTTP(ctx, req)
	}
	if u.HasWS() {
		return u.ExecuteWS(ctx, req)
	}
	return nil, fmt.Errorf("no endpoint configured for upstream %s", u.name)
}

// ExecuteHTTP sends a JSON-RPC request via HTTP
func (u *Upstream) ExecuteHTTP(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Response, error) {
	if u.rpcURL == "" {
		return nil, fmt.Errorf("HTTP RPC URL not configured")
	}

	reqBytes, err := req.Bytes()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.rpcURL, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := u.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Increment request counter after successful HTTP call
	u.IncrementRequestCount()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	rpcResp, err := jsonrpc.ParseResponse(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return rpcResp, nil
}

// ExecuteBatch sends a batch of JSON-RPC requests via HTTP
func (u *Upstream) ExecuteBatch(ctx context.Context, requests []*jsonrpc.Request) ([]*jsonrpc.Response, error) {
	if u.rpcURL == "" {
		return nil, fmt.Errorf("HTTP RPC URL not configured")
	}

	reqBytes, err := json.Marshal(requests)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal batch request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.rpcURL, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := u.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Increment request counter by number of requests in batch
	u.IncrementRequestCountBy(uint64(len(requests)))

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	responses, _, err := jsonrpc.ParseBatchResponse(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse batch response: %w", err)
	}

	return responses, nil
}

// ExecuteWS sends a JSON-RPC request via WebSocket
func (u *Upstream) ExecuteWS(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Response, error) {
	u.wsMu.Lock()
	if u.wsConn == nil {
		u.wsMu.Unlock()
		return nil, fmt.Errorf("WebSocket not connected")
	}

	// Create response channel
	u.wsReqID++
	reqID := u.wsReqID
	respChan := make(chan *jsonrpc.Response, 1)
	u.wsPending[reqID] = respChan

	// Modify request ID for tracking
	wsReq := req.Clone()
	wsReq.ID = jsonrpc.NewIDInt(reqID)

	reqBytes, err := wsReq.Bytes()
	if err != nil {
		delete(u.wsPending, reqID)
		u.wsMu.Unlock()
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	err = u.wsConn.WriteMessage(websocket.TextMessage, reqBytes)
	u.wsMu.Unlock()

	if err != nil {
		u.wsMu.Lock()
		delete(u.wsPending, reqID)
		u.wsMu.Unlock()
		return nil, fmt.Errorf("failed to send WS request: %w", err)
	}

	// Increment request counter after successful WS send
	u.IncrementRequestCount()

	// Wait for response with context
	select {
	case resp := <-respChan:
		// Restore original ID
		resp.ID = req.ID
		return resp, nil
	case <-ctx.Done():
		u.wsMu.Lock()
		delete(u.wsPending, reqID)
		u.wsMu.Unlock()
		return nil, ctx.Err()
	}
}

// ConnectWS establishes a WebSocket connection
func (u *Upstream) ConnectWS(ctx context.Context) error {
	if u.wsURL == "" {
		return fmt.Errorf("WebSocket URL not configured")
	}

	u.wsMu.Lock()
	defer u.wsMu.Unlock()

	if u.wsConn != nil {
		return nil // Already connected
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, u.wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect WebSocket: %w", err)
	}

	u.wsConn = conn
	u.logger.Debug().Msg("WebSocket connected")

	return nil
}

// DisconnectWS closes the WebSocket connection
func (u *Upstream) DisconnectWS() {
	u.wsMu.Lock()
	defer u.wsMu.Unlock()

	if u.wsConn != nil {
		u.wsConn.Close()
		u.wsConn = nil
		u.logger.Debug().Msg("WebSocket disconnected")
	}

	// Cancel all pending requests
	for id, ch := range u.wsPending {
		close(ch)
		delete(u.wsPending, id)
	}
}

// HandleWSMessage processes an incoming WebSocket message
// This should be called from a goroutine reading the WebSocket
func (u *Upstream) HandleWSMessage(data []byte) {
	// Try to parse as response first
	var resp jsonrpc.Response
	if err := json.Unmarshal(data, &resp); err == nil && resp.ID.Value() != nil {
		// It's a response to a request
		if idFloat, ok := resp.ID.Value().(float64); ok {
			reqID := int64(idFloat)
			u.wsMu.Lock()
			if ch, exists := u.wsPending[reqID]; exists {
				delete(u.wsPending, reqID)
				u.wsMu.Unlock()
				ch <- &resp
				return
			}
			u.wsMu.Unlock()
		}
	}

	// Otherwise it might be a subscription notification
	// This will be handled by the subscription manager
}

// GetWSConn returns the WebSocket connection (for subscription handling)
func (u *Upstream) GetWSConn() *websocket.Conn {
	u.wsMu.Lock()
	defer u.wsMu.Unlock()
	return u.wsConn
}

// Close closes all connections
func (u *Upstream) Close() {
	u.DisconnectWS()
	u.httpClient.CloseIdleConnections()
}
