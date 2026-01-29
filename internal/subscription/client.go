package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"rpcgofer/internal/jsonrpc"
	"rpcgofer/internal/upstream"
)

// ClientSession manages subscriptions for a single WebSocket client
type ClientSession struct {
	sendFunc      SendFunc
	subscriptions map[string]*ClientSubscription // subID -> subscription
	mu            sync.RWMutex
	maxSubs       int
	logger        zerolog.Logger
	dedup         *Deduplicator
	pool          *upstream.Pool
	closed        bool
	closeChan     chan struct{}
}

// NewClientSession creates a new ClientSession
func NewClientSession(sendFunc SendFunc, pool *upstream.Pool, maxSubs int, dedupSize int, logger zerolog.Logger) (*ClientSession, error) {
	dedup, err := NewDeduplicator(dedupSize)
	if err != nil {
		return nil, err
	}

	return &ClientSession{
		sendFunc:      sendFunc,
		subscriptions: make(map[string]*ClientSubscription),
		maxSubs:       maxSubs,
		logger:        logger,
		dedup:         dedup,
		pool:          pool,
		closeChan:     make(chan struct{}),
	}, nil
}

// Subscribe creates a new subscription
func (cs *ClientSession) Subscribe(ctx context.Context, subType SubscriptionType, params json.RawMessage) (string, error) {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return "", fmt.Errorf("session is closed")
	}

	if len(cs.subscriptions) >= cs.maxSubs {
		cs.mu.Unlock()
		return "", fmt.Errorf("maximum subscriptions reached (%d)", cs.maxSubs)
	}
	cs.mu.Unlock()

	// Generate subscription ID
	subID := generateSubID()

	// Create client subscription
	clientSub := NewClientSubscription(subID, subType, params)

	// Subscribe to all healthy upstreams with WebSocket
	upstreams := cs.pool.GetHealthyWithWS()
	if len(upstreams) == 0 {
		return "", fmt.Errorf("no healthy upstreams with WebSocket available")
	}

	cs.logger.Debug().
		Str("subID", subID).
		Str("type", string(subType)).
		Int("upstreams", len(upstreams)).
		Msg("creating subscription")

	// Create subscriptions on all upstreams
	for _, u := range upstreams {
		upSub, err := cs.subscribeToUpstream(ctx, u, subType, params)
		if err != nil {
			cs.logger.Warn().
				Err(err).
				Str("upstream", u.Name()).
				Str("subID", subID).
				Msg("failed to subscribe to upstream")
			continue
		}
		clientSub.AddUpstreamSub(u.Name(), upSub)

		// Start reading events from this upstream
		go cs.readUpstreamEvents(clientSub, u.Name(), upSub)
	}

	if len(clientSub.GetUpstreamSubs()) == 0 {
		return "", fmt.Errorf("failed to subscribe to any upstream")
	}

	// Store subscription
	cs.mu.Lock()
	cs.subscriptions[subID] = clientSub
	cs.mu.Unlock()

	// Start sender goroutine
	go cs.sendToClient(clientSub)

	return subID, nil
}

// subscribeToUpstream creates a subscription on a single upstream
func (cs *ClientSession) subscribeToUpstream(ctx context.Context, u *upstream.Upstream, subType SubscriptionType, params json.RawMessage) (*UpstreamSubscription, error) {
	if !u.HasWS() {
		return nil, fmt.Errorf("upstream has no WebSocket URL")
	}

	// Connect to upstream WebSocket
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, u.WSURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	// Build subscription request
	var subParams []interface{}
	subParams = append(subParams, string(subType))
	if params != nil && len(params) > 0 {
		var additionalParams interface{}
		if err := json.Unmarshal(params, &additionalParams); err == nil {
			subParams = append(subParams, additionalParams)
		}
	}

	req, err := jsonrpc.NewRequest("eth_subscribe", subParams, jsonrpc.NewIDInt(1))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	reqBytes, err := req.Bytes()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, reqBytes); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Read response
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, respData, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	conn.SetReadDeadline(time.Time{}) // Clear deadline

	var resp jsonrpc.Response
	if err := json.Unmarshal(respData, &resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.HasError() {
		conn.Close()
		return nil, fmt.Errorf("subscription error: %s", resp.Error.Message)
	}

	// Extract subscription ID
	var upstreamSubID string
	if err := json.Unmarshal(resp.Result, &upstreamSubID); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to parse subscription ID: %w", err)
	}

	return &UpstreamSubscription{
		ID:        upstreamSubID,
		Upstream:  u,
		Conn:      conn,
		closeChan: make(chan struct{}),
	}, nil
}

// readUpstreamEvents reads events from an upstream subscription
func (cs *ClientSession) readUpstreamEvents(clientSub *ClientSubscription, upstreamName string, upSub *UpstreamSubscription) {
	defer func() {
		upSub.Conn.Close()
		clientSub.RemoveUpstreamSub(upstreamName)
	}()

	for {
		select {
		case <-clientSub.CloseChan():
			return
		case <-upSub.closeChan:
			return
		case <-cs.closeChan:
			return
		default:
		}

		upSub.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, data, err := upSub.Conn.ReadMessage()
		if err != nil {
			cs.logger.Debug().
				Err(err).
				Str("upstream", upstreamName).
				Str("subID", clientSub.ID).
				Msg("upstream read error")
			return
		}

		// Parse notification
		var notification SubscriptionNotification
		if err := json.Unmarshal(data, &notification); err != nil {
			continue // Not a notification
		}

		if notification.Method != "eth_subscription" {
			continue
		}

		// Check for duplicates
		if cs.dedup.IsDuplicate(clientSub.Type, notification.Params.Result) {
			cs.logger.Debug().
				Str("upstream", upstreamName).
				Str("subID", clientSub.ID).
				Msg("duplicate event, skipping")
			continue
		}

		// Create client notification with client's subscription ID
		clientNotification := NewNotification(clientSub.ID, notification.Params.Result)
		notifBytes, err := clientNotification.Bytes()
		if err != nil {
			cs.logger.Warn().Err(err).Msg("failed to marshal notification")
			continue
		}

		// Send to client
		clientSub.Send(notifBytes)

		cs.logger.Debug().
			Str("upstream", upstreamName).
			Str("subID", clientSub.ID).
			Str("type", string(clientSub.Type)).
			Msg("forwarded event to client")
	}
}

// sendToClient sends messages from the subscription to the client
func (cs *ClientSession) sendToClient(clientSub *ClientSubscription) {
	for {
		select {
		case data := <-clientSub.SendChan():
			cs.sendFunc(data)
		case <-clientSub.CloseChan():
			return
		case <-cs.closeChan:
			return
		}
	}
}

// Unsubscribe removes a subscription
func (cs *ClientSession) Unsubscribe(subID string) error {
	cs.mu.Lock()
	clientSub, ok := cs.subscriptions[subID]
	if !ok {
		cs.mu.Unlock()
		return fmt.Errorf("subscription not found: %s", subID)
	}
	delete(cs.subscriptions, subID)
	cs.mu.Unlock()

	// Close the subscription
	clientSub.Close()

	// Unsubscribe from all upstreams
	for upstreamName, upSub := range clientSub.GetUpstreamSubs() {
		cs.unsubscribeFromUpstream(upSub)
		cs.logger.Debug().
			Str("upstream", upstreamName).
			Str("subID", subID).
			Msg("unsubscribed from upstream")
	}

	cs.logger.Debug().Str("subID", subID).Msg("subscription removed")
	return nil
}

// unsubscribeFromUpstream sends unsubscribe request to upstream
func (cs *ClientSession) unsubscribeFromUpstream(upSub *UpstreamSubscription) {
	close(upSub.closeChan)

	// Try to send unsubscribe request
	req, err := jsonrpc.NewRequest("eth_unsubscribe", []string{upSub.ID}, jsonrpc.NewIDInt(1))
	if err != nil {
		return
	}

	reqBytes, _ := req.Bytes()
	upSub.Conn.WriteMessage(websocket.TextMessage, reqBytes)
	upSub.Conn.Close()
}

// Close closes the session and all subscriptions
func (cs *ClientSession) Close() {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return
	}
	cs.closed = true
	close(cs.closeChan)

	subs := make([]*ClientSubscription, 0, len(cs.subscriptions))
	for _, sub := range cs.subscriptions {
		subs = append(subs, sub)
	}
	cs.subscriptions = make(map[string]*ClientSubscription)
	cs.mu.Unlock()

	// Close all subscriptions
	for _, sub := range subs {
		sub.Close()
		for _, upSub := range sub.GetUpstreamSubs() {
			cs.unsubscribeFromUpstream(upSub)
		}
	}

	cs.logger.Debug().Msg("client session closed")
}

// GetSubscriptionCount returns the number of active subscriptions
func (cs *ClientSession) GetSubscriptionCount() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.subscriptions)
}

// generateSubID generates a unique subscription ID
func generateSubID() string {
	return fmt.Sprintf("0x%x", time.Now().UnixNano())
}
