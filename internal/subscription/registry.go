package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/subscriptionregistry"
	"rpcgofer/internal/upstream"
)

// SubscriptionTarget is the interface an upstream implements; same as subscriptionregistry.SubscriptionTarget.
type SubscriptionTarget = subscriptionregistry.SubscriptionTarget

// subEntry holds subscribers and dedup for one active subscription key
type subEntry struct {
	subType   SubscriptionType
	params    json.RawMessage
	subs      map[string]Subscriber
	dedup     *Deduplicator
	upstreams map[string]string // upstreamName -> subID (for unsubscribe when subscription removed)
}

// Registry is the single source of truth for subscriptions: active (subType, params), subscribers, and registered upstreams.
// Upstreams register with a SubscriptionTarget; the registry subscribes them to all active subscriptions and pushes new ones.
// Clients and HealthMonitor Subscribe/Unsubscribe through the registry; events are delivered via DeliverEvent.
type Registry struct {
	mu sync.RWMutex

	// key -> subEntry (subscribers + dedup + upstreams subIDs for this key)
	active map[string]*subEntry
	// upstreamName -> target (for Subscribe/Unsubscribe calls)
	upstreams map[string]subscriptionregistry.SubscriptionTarget

	dedupSize int
	logger    zerolog.Logger
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewRegistry creates a new subscription registry.
func NewRegistry(dedupSize int, logger zerolog.Logger) *Registry {
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{
		active:    make(map[string]*subEntry),
		upstreams:  make(map[string]subscriptionregistry.SubscriptionTarget),
		dedupSize:  dedupSize,
		logger:     logger.With().Str("component", "subscription-registry").Logger(),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Close stops the registry and cancels its context.
func (r *Registry) Close() {
	r.cancel()
	r.mu.Lock()
	r.active = make(map[string]*subEntry)
	r.upstreams = make(map[string]subscriptionregistry.SubscriptionTarget)
	r.mu.Unlock()
	r.logger.Info().Msg("subscription registry closed")
}

func (r *Registry) generateKey(subType SubscriptionType, params json.RawMessage) string {
	if len(params) == 0 || string(params) == "null" {
		return string(subType) + ":"
	}
	hash := sha256.Sum256(params)
	return string(subType) + ":" + hex.EncodeToString(hash[:8])
}

// Subscribe adds a subscriber to a subscription (creates the subscription if new).
// If the subscription is new, the registry subscribes all currently registered upstreams to it.
func (r *Registry) Subscribe(ctx context.Context, subType SubscriptionType, params json.RawMessage, subscriber Subscriber) error {
	key := r.generateKey(subType, params)

	r.mu.Lock()
	entry, exists := r.active[key]
	if exists {
		entry.subs[subscriber.ID()] = subscriber
		r.mu.Unlock()
		r.logger.Debug().Str("key", key).Str("subscriberID", subscriber.ID()).Msg("subscriber added to existing subscription")
		return nil
	}

	dedup, err := NewDeduplicator(r.dedupSize)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	entry = &subEntry{
		subType:   subType,
		params:    params,
		subs:      map[string]Subscriber{subscriber.ID(): subscriber},
		dedup:     dedup,
		upstreams: make(map[string]string),
	}
	r.active[key] = entry

	// Copy list of upstreams to call without holding lock
	upstreamNames := make([]string, 0, len(r.upstreams))
	targets := make(map[string]subscriptionregistry.SubscriptionTarget, len(r.upstreams))
	for name, t := range r.upstreams {
		upstreamNames = append(upstreamNames, name)
		targets[name] = t
	}
	r.mu.Unlock()

	r.logger.Info().Str("key", key).Str("type", string(subType)).Str("subscriberID", subscriber.ID()).Msg("created new subscription")

	// Subscribe each registered upstream (no lock held to avoid deadlock)
	for _, name := range upstreamNames {
		t := targets[name]
		subID, err := t.Subscribe(ctx, subscriptionregistry.SubscriptionType(subType), params)
		if err != nil {
			r.logger.Warn().Err(err).Str("upstream", name).Str("key", key).Msg("failed to subscribe upstream to new subscription")
			continue
		}
		r.mu.Lock()
		if e, ok := r.active[key]; ok {
			e.upstreams[name] = subID
		}
		r.mu.Unlock()
	}
	return nil
}

// Unsubscribe removes a subscriber. If no subscribers remain, the subscription is removed and all upstreams are unsubscribed.
func (r *Registry) Unsubscribe(subType SubscriptionType, params json.RawMessage, subscriberID string) error {
	key := r.generateKey(subType, params)

	r.mu.Lock()
	entry, exists := r.active[key]
	if !exists {
		r.mu.Unlock()
		return nil
	}
	delete(entry.subs, subscriberID)
	if len(entry.subs) > 0 {
		r.mu.Unlock()
		r.logger.Debug().Str("key", key).Str("subscriberID", subscriberID).Int("remaining", len(entry.subs)).Msg("subscriber removed")
		return nil
	}

	// Last subscriber: remove subscription and collect upstreams to unsubscribe
	toUnsub := make(map[string]string, len(entry.upstreams))
	for name, subID := range entry.upstreams {
		toUnsub[name] = subID
	}
	delete(r.active, key)
	targets := make(map[string]SubscriptionTarget, len(toUnsub))
	for name := range toUnsub {
		if t, ok := r.upstreams[name]; ok {
			targets[name] = t
		}
	}
	r.mu.Unlock()

	r.logger.Info().Str("key", key).Msg("closed subscription (no more subscribers)")

	for name, subID := range toUnsub {
		if t := targets[name]; t != nil {
			t.Unsubscribe(subID)
		}
	}
	return nil
}

// Register registers an upstream with the given target. The registry immediately subscribes the target to all currently active subscriptions (without holding the lock during Subscribe calls).
func (r *Registry) Register(upstreamName string, target subscriptionregistry.SubscriptionTarget) {
	r.mu.Lock()
	if old, exists := r.upstreams[upstreamName]; exists {
		// Replace: remove old subIDs from all entries (we don't call Unsubscribe on dead connection)
		for _, entry := range r.active {
			delete(entry.upstreams, upstreamName)
		}
		_ = old
	}
	r.upstreams[upstreamName] = target
	// Copy list of active subscription keys and params to subscribe (no lock during Subscribe)
	toSub := make([]struct {
		key    string
		subType SubscriptionType
		params  json.RawMessage
	}, 0, len(r.active))
	for key, entry := range r.active {
		toSub = append(toSub, struct {
			key     string
			subType SubscriptionType
			params  json.RawMessage
		}{key, entry.subType, entry.params})
	}
	r.mu.Unlock()

	r.logger.Info().Str("upstream", upstreamName).Int("activeSubs", len(toSub)).Msg("upstream registered")

	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	defer cancel()
	for _, item := range toSub {
		subID, err := target.Subscribe(ctx, subscriptionregistry.SubscriptionType(item.subType), item.params)
		if err != nil {
			r.logger.Warn().Err(err).Str("upstream", upstreamName).Str("key", item.key).Msg("failed to subscribe upstream")
			continue
		}
		r.mu.Lock()
		if entry, ok := r.active[item.key]; ok {
			entry.upstreams[upstreamName] = subID
		}
		r.mu.Unlock()
	}
}

// Unregister removes the upstream and all its subscriptions from the registry. Call when the upstream disconnects.
func (r *Registry) Unregister(upstreamName string) {
	r.mu.Lock()
	delete(r.upstreams, upstreamName)
	for _, entry := range r.active {
		delete(entry.upstreams, upstreamName)
	}
	r.mu.Unlock()
	r.logger.Info().Str("upstream", upstreamName).Msg("upstream unregistered")
}

// DeliverEvent delivers an event from an upstream to all subscribers of the matching subscription. Dedup and DeliverFirst order are applied.
// Implements subscriptionregistry.Registry.
func (r *Registry) DeliverEvent(upstreamName string, subType subscriptionregistry.SubscriptionType, params json.RawMessage, result json.RawMessage) {
	subTypeLocal := SubscriptionType(subType)
	key := r.generateKey(subTypeLocal, params)

	r.mu.RLock()
	entry, exists := r.active[key]
	if !exists {
		r.mu.RUnlock()
		return
	}
	subs := make([]Subscriber, 0, len(entry.subs))
	for _, s := range entry.subs {
		subs = append(subs, s)
	}
	dedup := entry.dedup
	r.mu.RUnlock()

	isDuplicate := dedup.IsDuplicate(subTypeLocal, result)

	sort.Slice(subs, func(i, j int) bool {
		di, dj := subs[i].DeliverFirst(), subs[j].DeliverFirst()
		if di != dj {
			return di
		}
		return subs[i].ID() < subs[j].ID()
	})

	event := SubscriptionEvent{
		UpstreamName: upstreamName,
		SubType:      subTypeLocal,
		Result:       result,
	}

	deliver := func(sub Subscriber) {
		if isDuplicate && !sub.SkipDedup() {
			return
		}
		start := time.Now()
		sub.OnEvent(event)
		if d := time.Since(start); d > time.Second {
			r.logger.Warn().Str("subscriber", sub.ID()).Str("upstream", upstreamName).Dur("duration", d).Msg("subscriber delivery slow")
		}
	}
	for _, sub := range subs {
		if sub.DeliverFirst() {
			deliver(sub)
		}
	}
	for _, sub := range subs {
		if !sub.DeliverFirst() {
			deliver(sub)
		}
	}
}

// RegistryNewHeadsProviderAdapter adapts Registry to upstream.NewHeadsProvider for health monitoring.
type RegistryNewHeadsProviderAdapter struct {
	registry *Registry
}

// NewRegistryNewHeadsProviderAdapter creates an adapter that uses the registry for newHeads.
func NewRegistryNewHeadsProviderAdapter(registry *Registry) *RegistryNewHeadsProviderAdapter {
	return &RegistryNewHeadsProviderAdapter{registry: registry}
}

// SubscribeNewHeads implements upstream.NewHeadsProvider
func (a *RegistryNewHeadsProviderAdapter) SubscribeNewHeads(ctx context.Context, subscriber upstream.NewHeadsSubscriber) error {
	wrapper := &newHeadsSubscriberWrapper{subscriber: subscriber}
	return a.registry.Subscribe(ctx, SubTypeNewHeads, nil, wrapper)
}

// UnsubscribeNewHeads implements upstream.NewHeadsProvider
func (a *RegistryNewHeadsProviderAdapter) UnsubscribeNewHeads(subscriberID string) error {
	return a.registry.Unsubscribe(SubTypeNewHeads, nil, subscriberID)
}

// newHeadsSubscriberWrapper wraps upstream.NewHeadsSubscriber to implement Subscriber (used by RegistryNewHeadsProviderAdapter).
type newHeadsSubscriberWrapper struct {
	subscriber upstream.NewHeadsSubscriber
}

func (w *newHeadsSubscriberWrapper) OnEvent(event SubscriptionEvent) {
	w.subscriber.OnBlock(event.UpstreamName, event.Result)
}

func (w *newHeadsSubscriberWrapper) ID() string {
	return w.subscriber.ID()
}

func (w *newHeadsSubscriberWrapper) SkipDedup() bool {
	return true
}

func (w *newHeadsSubscriberWrapper) DeliverFirst() bool {
	return w.subscriber.DeliverFirst()
}
