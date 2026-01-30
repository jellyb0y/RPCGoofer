package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"rpcgofer/internal/jsonrpc"
	"rpcgofer/internal/upstream"
)

// SharedSubscriptionManager manages shared subscriptions across all clients and internal consumers
type SharedSubscriptionManager struct {
	subscriptions map[string]*SharedSubscription // key -> shared subscription
	pool          *upstream.Pool
	dedupSize     int
	mu            sync.RWMutex
	logger        zerolog.Logger
	ctx           context.Context
	cancel        context.CancelFunc
}

// NewSharedSubscriptionManager creates a new SharedSubscriptionManager
func NewSharedSubscriptionManager(pool *upstream.Pool, dedupSize int, logger zerolog.Logger) *SharedSubscriptionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &SharedSubscriptionManager{
		subscriptions: make(map[string]*SharedSubscription),
		pool:          pool,
		dedupSize:     dedupSize,
		logger:        logger.With().Str("component", "shared-subscription-manager").Logger(),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Subscribe adds a subscriber to a shared subscription, creating it if necessary
func (m *SharedSubscriptionManager) Subscribe(ctx context.Context, subType SubscriptionType, params json.RawMessage, subscriber Subscriber) error {
	key := m.generateKey(subType, params)

	m.mu.Lock()

	// Check if shared subscription already exists
	shared, exists := m.subscriptions[key]
	if exists {
		shared.AddSubscriber(subscriber)
		m.mu.Unlock()
		m.logger.Debug().
			Str("key", key).
			Str("subscriberID", subscriber.ID()).
			Msg("subscriber added to existing shared subscription")
		return nil
	}

	// Create new shared subscription
	shared, err := m.createSharedSubscription(ctx, subType, params, key)
	if err != nil {
		m.mu.Unlock()
		return err
	}

	shared.AddSubscriber(subscriber)
	m.subscriptions[key] = shared
	m.mu.Unlock()

	m.logger.Info().
		Str("key", key).
		Str("type", string(subType)).
		Str("subscriberID", subscriber.ID()).
		Msg("created new shared subscription")

	return nil
}

// Unsubscribe removes a subscriber from a shared subscription
func (m *SharedSubscriptionManager) Unsubscribe(subType SubscriptionType, params json.RawMessage, subscriberID string) error {
	key := m.generateKey(subType, params)

	m.mu.Lock()
	defer m.mu.Unlock()

	shared, exists := m.subscriptions[key]
	if !exists {
		return nil // Subscription doesn't exist, nothing to do
	}

	shared.RemoveSubscriber(subscriberID)

	// If no more subscribers, close and remove the shared subscription
	if shared.SubscriberCount() == 0 {
		shared.Close()
		delete(m.subscriptions, key)
		m.logger.Info().
			Str("key", key).
			Msg("closed shared subscription (no more subscribers)")
	} else {
		m.logger.Debug().
			Str("key", key).
			Str("subscriberID", subscriberID).
			Int("remainingSubscribers", shared.SubscriberCount()).
			Msg("subscriber removed from shared subscription")
	}

	return nil
}

// Close closes the manager and all shared subscriptions
func (m *SharedSubscriptionManager) Close() {
	m.cancel()

	m.mu.Lock()
	defer m.mu.Unlock()

	for key, shared := range m.subscriptions {
		shared.Close()
		delete(m.subscriptions, key)
	}

	m.logger.Info().Msg("shared subscription manager closed")
}

// GetPool returns the upstream pool
func (m *SharedSubscriptionManager) GetPool() *upstream.Pool {
	return m.pool
}

// generateKey generates a unique key for a subscription type and params
func (m *SharedSubscriptionManager) generateKey(subType SubscriptionType, params json.RawMessage) string {
	if len(params) == 0 || string(params) == "null" {
		return string(subType) + ":"
	}
	hash := sha256.Sum256(params)
	return string(subType) + ":" + hex.EncodeToString(hash[:8])
}

// createSharedSubscription creates a new shared subscription with upstream connections
func (m *SharedSubscriptionManager) createSharedSubscription(ctx context.Context, subType SubscriptionType, params json.RawMessage, key string) (*SharedSubscription, error) {
	upstreams := m.pool.GetWithWS()
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no upstreams with WebSocket available")
	}

	dedup, err := NewDeduplicator(m.dedupSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create deduplicator: %w", err)
	}

	shared := &SharedSubscription{
		key:          key,
		subType:      subType,
		params:       params,
		upstreamSubs: make(map[string]*sharedUpstreamSub),
		subscribers:  make(map[string]Subscriber),
		dedup:        dedup,
		logger:       m.logger.With().Str("key", key).Logger(),
		closeChan:    make(chan struct{}),
		pool:         m.pool,
	}

	// Subscribe to all upstreams with WebSocket
	for _, u := range upstreams {
		upSub, err := shared.subscribeToUpstream(ctx, u, subType, params)
		if err != nil {
			m.logger.Warn().
				Err(err).
				Str("upstream", u.Name()).
				Str("key", key).
				Msg("failed to subscribe to upstream")
			continue
		}
		shared.upstreamSubs[u.Name()] = upSub

		// Start reading events from this upstream
		go shared.readUpstreamEvents(u.Name(), upSub)
	}

	if len(shared.upstreamSubs) == 0 {
		return nil, fmt.Errorf("failed to subscribe to any upstream")
	}

	return shared, nil
}

// SharedSubscription represents a subscription shared between multiple subscribers
type SharedSubscription struct {
	key          string
	subType      SubscriptionType
	params       json.RawMessage
	upstreamSubs map[string]*sharedUpstreamSub // upstream name -> subscription
	subscribers  map[string]Subscriber         // subscriber ID -> subscriber
	dedup        *Deduplicator
	mu           sync.RWMutex
	logger       zerolog.Logger
	closeChan    chan struct{}
	closed       bool
	pool         *upstream.Pool
}

// sharedUpstreamSub represents a subscription to a single upstream
type sharedUpstreamSub struct {
	id        string          // Subscription ID from upstream
	conn      *websocket.Conn // WebSocket connection
	closeChan chan struct{}
}

// AddSubscriber adds a subscriber to this shared subscription
func (s *SharedSubscription) AddSubscriber(subscriber Subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribers[subscriber.ID()] = subscriber
}

// RemoveSubscriber removes a subscriber from this shared subscription
func (s *SharedSubscription) RemoveSubscriber(subscriberID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscribers, subscriberID)
}

// SubscriberCount returns the number of subscribers
func (s *SharedSubscription) SubscriberCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subscribers)
}

// Close closes the shared subscription and all upstream connections
func (s *SharedSubscription) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.closeChan)
	s.mu.Unlock()

	// Close all upstream subscriptions
	for upstreamName, upSub := range s.upstreamSubs {
		s.unsubscribeFromUpstream(upstreamName, upSub)
	}

	s.logger.Debug().Msg("shared subscription closed")
}

// subscribeToUpstream creates a subscription on a single upstream
func (s *SharedSubscription) subscribeToUpstream(ctx context.Context, u *upstream.Upstream, subType SubscriptionType, params json.RawMessage) (*sharedUpstreamSub, error) {
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
	if params != nil && len(params) > 0 && string(params) != "null" {
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

	return &sharedUpstreamSub{
		id:        upstreamSubID,
		conn:      conn,
		closeChan: make(chan struct{}),
	}, nil
}

// readUpstreamEvents reads events from an upstream subscription and broadcasts to all subscribers
func (s *SharedSubscription) readUpstreamEvents(upstreamName string, upSub *sharedUpstreamSub) {
	defer func() {
		upSub.conn.Close()
		s.mu.Lock()
		delete(s.upstreamSubs, upstreamName)
		s.mu.Unlock()
	}()

	for {
		select {
		case <-s.closeChan:
			return
		case <-upSub.closeChan:
			return
		default:
		}

		upSub.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, data, err := upSub.conn.ReadMessage()
		if err != nil {
			s.logger.Debug().
				Err(err).
				Str("upstream", upstreamName).
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
		if s.dedup.IsDuplicate(s.subType, notification.Params.Result) {
			s.logger.Debug().
				Str("upstream", upstreamName).
				Msg("duplicate event, skipping")
			continue
		}

		// Create event and broadcast to all subscribers
		event := SubscriptionEvent{
			UpstreamName: upstreamName,
			SubType:      s.subType,
			Result:       notification.Params.Result,
		}

		s.broadcastEvent(event)

		s.logger.Debug().
			Str("upstream", upstreamName).
			Str("type", string(s.subType)).
			Msg("broadcasted event to subscribers")
	}
}

// broadcastEvent sends an event to all subscribers
func (s *SharedSubscription) broadcastEvent(event SubscriptionEvent) {
	s.mu.RLock()
	subscribers := make([]Subscriber, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		subscribers = append(subscribers, sub)
	}
	s.mu.RUnlock()

	for _, sub := range subscribers {
		sub.OnEvent(event)
	}
}

// unsubscribeFromUpstream sends unsubscribe request to upstream and closes connection
func (s *SharedSubscription) unsubscribeFromUpstream(upstreamName string, upSub *sharedUpstreamSub) {
	close(upSub.closeChan)

	// Try to send unsubscribe request
	req, err := jsonrpc.NewRequest("eth_unsubscribe", []string{upSub.id}, jsonrpc.NewIDInt(1))
	if err != nil {
		upSub.conn.Close()
		return
	}

	reqBytes, _ := req.Bytes()
	upSub.conn.WriteMessage(websocket.TextMessage, reqBytes)
	upSub.conn.Close()

	s.logger.Debug().
		Str("upstream", upstreamName).
		Msg("unsubscribed from upstream")
}

// GetUpstream returns the upstream by name from the pool
func (s *SharedSubscription) GetUpstream(name string) *upstream.Upstream {
	return s.pool.GetByName(name)
}

// NewHeadsProviderAdapter adapts SharedSubscriptionManager to upstream.NewHeadsProvider interface
type NewHeadsProviderAdapter struct {
	mgr *SharedSubscriptionManager
}

// NewNewHeadsProviderAdapter creates a new adapter
func NewNewHeadsProviderAdapter(mgr *SharedSubscriptionManager) *NewHeadsProviderAdapter {
	return &NewHeadsProviderAdapter{mgr: mgr}
}

// SubscribeNewHeads implements upstream.NewHeadsProvider
func (a *NewHeadsProviderAdapter) SubscribeNewHeads(ctx context.Context, subscriber upstream.NewHeadsSubscriber) error {
	// Create wrapper that adapts upstream.NewHeadsSubscriber to subscription.Subscriber
	wrapper := &newHeadsSubscriberWrapper{subscriber: subscriber}
	return a.mgr.Subscribe(ctx, SubTypeNewHeads, nil, wrapper)
}

// UnsubscribeNewHeads implements upstream.NewHeadsProvider
func (a *NewHeadsProviderAdapter) UnsubscribeNewHeads(subscriberID string) error {
	return a.mgr.Unsubscribe(SubTypeNewHeads, nil, subscriberID)
}

// newHeadsSubscriberWrapper wraps upstream.NewHeadsSubscriber to implement subscription.Subscriber
type newHeadsSubscriberWrapper struct {
	subscriber upstream.NewHeadsSubscriber
}

// OnEvent implements Subscriber interface
func (w *newHeadsSubscriberWrapper) OnEvent(event SubscriptionEvent) {
	w.subscriber.OnBlock(event.UpstreamName, event.Result)
}

// ID implements Subscriber interface
func (w *newHeadsSubscriberWrapper) ID() string {
	return w.subscriber.ID()
}
