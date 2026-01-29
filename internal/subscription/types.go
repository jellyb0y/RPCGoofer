package subscription

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"

	"rpcgofer/internal/upstream"
)

// SendFunc is a callback function for sending data to the client
type SendFunc func(data []byte)

// SubscriptionType represents the type of subscription
type SubscriptionType string

const (
	SubTypeNewHeads               SubscriptionType = "newHeads"
	SubTypeLogs                   SubscriptionType = "logs"
	SubTypeNewPendingTransactions SubscriptionType = "newPendingTransactions"
	SubTypeSyncing                SubscriptionType = "syncing"
)

// ClientSubscription represents a client's subscription
type ClientSubscription struct {
	ID               string           // Subscription ID returned to client
	Type             SubscriptionType // Subscription type
	Params           json.RawMessage  // Additional parameters (e.g., log filter)
	UpstreamSubs     map[string]*UpstreamSubscription // upstream name -> subscription
	mu               sync.RWMutex
	sendChan         chan []byte     // Channel for sending messages to client
	closeChan        chan struct{}   // Channel to signal closure
	closed           bool
}

// UpstreamSubscription represents a subscription to an upstream
type UpstreamSubscription struct {
	ID       string             // Subscription ID from upstream
	Upstream *upstream.Upstream // The upstream
	Conn     *websocket.Conn    // WebSocket connection to upstream
	closeChan chan struct{}     // Channel to signal closure
}

// NewClientSubscription creates a new ClientSubscription
func NewClientSubscription(id string, subType SubscriptionType, params json.RawMessage) *ClientSubscription {
	return &ClientSubscription{
		ID:           id,
		Type:         subType,
		Params:       params,
		UpstreamSubs: make(map[string]*UpstreamSubscription),
		sendChan:     make(chan []byte, 100),
		closeChan:    make(chan struct{}),
	}
}

// AddUpstreamSub adds an upstream subscription
func (cs *ClientSubscription) AddUpstreamSub(upstreamName string, sub *UpstreamSubscription) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.UpstreamSubs[upstreamName] = sub
}

// RemoveUpstreamSub removes an upstream subscription
func (cs *ClientSubscription) RemoveUpstreamSub(upstreamName string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.UpstreamSubs, upstreamName)
}

// GetUpstreamSubs returns all upstream subscriptions
func (cs *ClientSubscription) GetUpstreamSubs() map[string]*UpstreamSubscription {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	
	result := make(map[string]*UpstreamSubscription)
	for k, v := range cs.UpstreamSubs {
		result[k] = v
	}
	return result
}

// Send sends data to the client (non-blocking)
func (cs *ClientSubscription) Send(data []byte) bool {
	select {
	case cs.sendChan <- data:
		return true
	case <-cs.closeChan:
		return false
	default:
		// Channel full, drop message
		return false
	}
}

// Close closes the subscription
func (cs *ClientSubscription) Close() {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return
	}
	cs.closed = true
	close(cs.closeChan)
	cs.mu.Unlock()
}

// IsClosed returns true if the subscription is closed
func (cs *ClientSubscription) IsClosed() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.closed
}

// SendChan returns the send channel
func (cs *ClientSubscription) SendChan() <-chan []byte {
	return cs.sendChan
}

// CloseChan returns the close channel
func (cs *ClientSubscription) CloseChan() <-chan struct{} {
	return cs.closeChan
}

// SubscriptionNotification represents a subscription notification
type SubscriptionNotification struct {
	JSONRPC string             `json:"jsonrpc"`
	Method  string             `json:"method"`
	Params  NotificationParams `json:"params"`
}

// NotificationParams contains the notification parameters
type NotificationParams struct {
	Subscription string          `json:"subscription"`
	Result       json.RawMessage `json:"result"`
}

// NewNotification creates a new subscription notification
func NewNotification(subID string, result json.RawMessage) *SubscriptionNotification {
	return &SubscriptionNotification{
		JSONRPC: "2.0",
		Method:  "eth_subscription",
		Params: NotificationParams{
			Subscription: subID,
			Result:       result,
		},
	}
}

// Bytes returns the notification as JSON bytes
func (n *SubscriptionNotification) Bytes() ([]byte, error) {
	return json.Marshal(n)
}
