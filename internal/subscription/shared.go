package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/upstream"
)

// SharedSubscriptionManager manages shared subscriptions across all clients and internal consumers
type SharedSubscriptionManager struct {
	subscriptions     map[string]*SharedSubscription // key -> shared subscription
	pool              *upstream.Pool
	dedupSize         int
	messageTimeout    time.Duration
	reconnectInterval time.Duration
	mu                sync.RWMutex
	logger            zerolog.Logger
	ctx               context.Context
	cancel            context.CancelFunc
}

// NewSharedSubscriptionManager creates a new SharedSubscriptionManager
func NewSharedSubscriptionManager(pool *upstream.Pool, dedupSize int, messageTimeout time.Duration, reconnectInterval time.Duration, logger zerolog.Logger) *SharedSubscriptionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &SharedSubscriptionManager{
		subscriptions:     make(map[string]*SharedSubscription),
		pool:              pool,
		dedupSize:         dedupSize,
		messageTimeout:    messageTimeout,
		reconnectInterval: reconnectInterval,
		logger:            logger.With().Str("component", "shared-subscription-manager").Logger(),
		ctx:               ctx,
		cancel:            cancel,
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

// AddUpstreamToExistingSubscriptions adds the upstream to all existing shared subscriptions.
// Call when an upstream's WebSocket connects (e.g. late-connecting or after reconnect).
func (m *SharedSubscriptionManager) AddUpstreamToExistingSubscriptions(u *upstream.Upstream) {
	m.mu.RLock()
	subs := make([]*SharedSubscription, 0, len(m.subscriptions))
	for _, shared := range m.subscriptions {
		subs = append(subs, shared)
	}
	m.mu.RUnlock()

	for _, shared := range subs {
		if err := shared.AddUpstream(m.ctx, u); err != nil {
			m.logger.Warn().
				Err(err).
				Str("upstream", u.Name()).
				Str("key", shared.key).
				Msg("failed to add upstream to existing shared subscription")
		}
	}
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
	upstreams := m.pool.GetConnectedWithWS()
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no upstreams with WebSocket connected")
	}

	dedup, err := NewDeduplicator(m.dedupSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create deduplicator: %w", err)
	}

	shared := &SharedSubscription{
		key:               key,
		subType:           subType,
		params:            params,
		upstreamSubs:      make(map[string]*sharedUpstreamSub),
		subscribers:       make(map[string]Subscriber),
		dedup:             dedup,
		logger:            m.logger.With().Str("key", key).Logger(),
		closeChan:         make(chan struct{}),
		pool:              m.pool,
		messageTimeout:    m.messageTimeout,
		reconnectInterval: m.reconnectInterval,
		managerCtx:        m.ctx,
	}

	// Subscribe to all upstreams with WebSocket (uses shared connection owned by upstream)
	for _, u := range upstreams {
		upSub, err := shared.registerUpstreamSubscription(ctx, u, subType, params)
		if err != nil {
			m.logger.Warn().
				Err(err).
				Str("upstream", u.Name()).
				Str("key", key).
				Msg("failed to subscribe to upstream")
			continue
		}
		shared.upstreamSubs[u.Name()] = upSub
	}

	if len(shared.upstreamSubs) == 0 {
		return nil, fmt.Errorf("failed to subscribe to any upstream")
	}

	return shared, nil
}

// SharedSubscription represents a subscription shared between multiple subscribers
type SharedSubscription struct {
	key               string
	subType           SubscriptionType
	params            json.RawMessage
	upstreamSubs      map[string]*sharedUpstreamSub // upstream name -> subscription
	subscribers       map[string]Subscriber         // subscriber ID -> subscriber
	dedup             *Deduplicator
	mu                sync.RWMutex
	logger            zerolog.Logger
	closeChan         chan struct{}
	closed            bool
	pool              *upstream.Pool
	messageTimeout    time.Duration
	reconnectInterval time.Duration
	managerCtx        context.Context
}

// sharedUpstreamSub represents a subscription to a single upstream via the shared WebSocket connection
type sharedUpstreamSub struct {
	id          string
	closeChan   chan struct{}
	unsubscribe func()
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

// AddUpstream adds an upstream to this shared subscription (e.g. when it connects later).
// Idempotent: if the upstream is already subscribed, returns nil.
func (s *SharedSubscription) AddUpstream(ctx context.Context, u *upstream.Upstream) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if _, exists := s.upstreamSubs[u.Name()]; exists {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	upSub, err := s.registerUpstreamSubscription(ctx, u, s.subType, s.params)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.unsubscribeFromUpstream(u.Name(), upSub)
		return nil
	}
	s.upstreamSubs[u.Name()] = upSub
	return nil
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

// registerUpstreamSubscription subscribes to an upstream via its shared WebSocket connection
func (s *SharedSubscription) registerUpstreamSubscription(ctx context.Context, u *upstream.Upstream, subType SubscriptionType, params json.RawMessage) (*sharedUpstreamSub, error) {
	if !u.HasWS() {
		return nil, fmt.Errorf("upstream has no WebSocket URL")
	}

	upstreamName := u.Name()

	eventHandler := func(result json.RawMessage) {
		isDuplicate := s.dedup.IsDuplicate(subType, result)
		event := SubscriptionEvent{
			UpstreamName: upstreamName,
			SubType:      subType,
			Result:       result,
		}
		s.broadcastEvent(event, isDuplicate)

		if !isDuplicate {
			s.logger.Debug().
				Str("upstream", upstreamName).
				Str("type", string(subType)).
				Msg("broadcasted event to subscribers")
		}
	}

	subID, err := u.SubscribeWS(ctx, string(subType), params, eventHandler)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	upSub := &sharedUpstreamSub{
		id:        subID,
		closeChan: make(chan struct{}),
		unsubscribe: func() {
			u.UnsubscribeWS(subID)
		},
	}

	return upSub, nil
}

// cleanupUpstreamSub cleans up an upstream subscription
func (s *SharedSubscription) cleanupUpstreamSub(upstreamName string, upSub *sharedUpstreamSub) {
	close(upSub.closeChan)
	if upSub.unsubscribe != nil {
		upSub.unsubscribe()
	}
	s.mu.Lock()
	delete(s.upstreamSubs, upstreamName)
	s.mu.Unlock()
}

// broadcastEvent sends an event to all subscribers.
// State-updating subscribers (e.g. health-monitor) are called first so block updates
// are applied before client subscribers that may block in waitForMainBlock.
func (s *SharedSubscription) broadcastEvent(event SubscriptionEvent, isDuplicate bool) {
	s.mu.RLock()
	subscribers := make([]Subscriber, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		subscribers = append(subscribers, sub)
	}
	s.mu.RUnlock()

	sort.Slice(subscribers, func(i, j int) bool {
		di, dj := subscribers[i].DeliverFirst(), subscribers[j].DeliverFirst()
		if di != dj {
			return di
		}
		return subscribers[i].ID() < subscribers[j].ID()
	})

	s.logger.Debug().
		Str("upstream", event.UpstreamName).
		Int("subscribers", len(subscribers)).
		Bool("isDuplicate", isDuplicate).
		Msg("broadcastEvent start")

	for _, sub := range subscribers {
		if isDuplicate && !sub.SkipDedup() {
			continue
		}
		subID := sub.ID()
		start := time.Now()
		sub.OnEvent(event)
		dur := time.Since(start)
		if dur > time.Second {
			s.logger.Warn().
				Str("subscriber", subID).
				Str("upstream", event.UpstreamName).
				Dur("duration", dur).
				Msg("broadcastEvent subscriber took long")
		}
	}
}

// unsubscribeFromUpstream unsubscribes from upstream and cleans up
func (s *SharedSubscription) unsubscribeFromUpstream(upstreamName string, upSub *sharedUpstreamSub) {
	close(upSub.closeChan)
	if upSub.unsubscribe != nil {
		upSub.unsubscribe()
	}

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

// SkipDedup implements Subscriber interface
func (w *newHeadsSubscriberWrapper) SkipDedup() bool {
	return true // HealthMonitor needs all events from all upstreams
}

// DeliverFirst implements Subscriber interface
func (w *newHeadsSubscriberWrapper) DeliverFirst() bool {
	return w.subscriber.DeliverFirst()
}
