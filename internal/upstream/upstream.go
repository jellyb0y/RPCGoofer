package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
	"rpcgofer/internal/jsonrpc"
)

// Upstream represents a single upstream RPC endpoint
type Upstream struct {
	name     string
	rpcURL   string
	wsURL    string
	weight   int
	role     Role
	preferWS bool

	httpClient *http.Client
	status     *Status
	logger     zerolog.Logger

	wsClient *UpstreamWSClient
}

// Config for creating a new Upstream
type Config struct {
	Name           string
	RPCURL         string
	WSURL          string
	Weight         int
	Role           Role
	PreferWS       bool
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
		preferWS:   cfg.PreferWS,
		httpClient: httpClient,
		status:     NewStatus(),
		logger:     cfg.Logger.With().Str("upstream", cfg.Name).Logger(),
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
		PreferWS:       cfg.PreferWS,
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

// IncrementBatchCount increments the coalesced batch counter for this upstream
func (u *Upstream) IncrementBatchCount() {
	u.status.IncrementBatchCount()
}

// SwapBatchCount returns the current batch count and resets it to zero
func (u *Upstream) SwapBatchCount() uint64 {
	return u.status.SwapBatchCount()
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

// Execute sends a JSON-RPC request and returns the response.
// When preferWS is true and both rpcUrl and wsUrl are configured, uses WebSocket.
// Otherwise prefers HTTP RPC, falls back to WebSocket if HTTP is not available.
func (u *Upstream) Execute(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Response, error) {
	if u.preferWS && u.HasWS() {
		return u.ExecuteWS(ctx, req)
	}
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

	// Increment request counter (one HTTP call = one request to upstream)
	u.IncrementRequestCount()

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
	if u.wsClient == nil {
		return nil, fmt.Errorf("WebSocket not connected")
	}
	return u.wsClient.SendRequest(ctx, req)
}

// StartWS establishes the WebSocket connection for this upstream. Called by Pool at startup.
func (u *Upstream) StartWS(ctx context.Context, messageTimeout time.Duration, reconnectInterval time.Duration) error {
	if u.wsURL == "" {
		return fmt.Errorf("WebSocket URL not configured")
	}
	if u.wsClient != nil {
		return nil
	}

	u.wsClient = NewUpstreamWSClient(u.wsURL, messageTimeout, reconnectInterval, u, u.logger)
	return u.wsClient.Connect(ctx)
}

// SubscribeWS subscribes to an event type on the upstream WebSocket. Returns the subscription ID.
func (u *Upstream) SubscribeWS(ctx context.Context, subType string, params json.RawMessage, onEvent func(json.RawMessage)) (string, error) {
	if u.wsClient == nil {
		return "", fmt.Errorf("WebSocket not connected")
	}
	return u.wsClient.Subscribe(ctx, subType, params, onEvent)
}

// UnsubscribeWS unsubscribes from an event on the upstream WebSocket.
func (u *Upstream) UnsubscribeWS(subID string) {
	if u.wsClient != nil {
		u.wsClient.Unsubscribe(subID)
	}
}

// Close closes all connections
func (u *Upstream) Close() {
	if u.wsClient != nil {
		u.wsClient.Close()
		u.wsClient = nil
	}
	u.httpClient.CloseIdleConnections()
}
