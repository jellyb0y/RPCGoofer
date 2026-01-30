package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/jsonrpc"
	"rpcgofer/internal/upstream"
)

const (
	// waitForMainBlockPollInterval is how often to check if main has the block
	waitForMainBlockPollInterval = 50 * time.Millisecond
)

// ClientSession manages subscriptions for a single WebSocket client
type ClientSession struct {
	sendFunc      SendFunc
	subscriptions map[string]*clientSubscriptionInfo // subID -> subscription info
	mu            sync.RWMutex
	maxSubs       int
	logger        zerolog.Logger
	pool          *upstream.Pool
	sharedSubMgr  *SharedSubscriptionManager
	closed        bool
	closeChan     chan struct{}
}

// clientSubscriptionInfo holds information about a client's subscription
type clientSubscriptionInfo struct {
	subID      string
	subType    SubscriptionType
	params     json.RawMessage
	subscriber *clientSubscriber
}

// NewClientSession creates a new ClientSession
func NewClientSession(sendFunc SendFunc, pool *upstream.Pool, sharedSubMgr *SharedSubscriptionManager, maxSubs int, dedupSize int, logger zerolog.Logger) (*ClientSession, error) {
	return &ClientSession{
		sendFunc:      sendFunc,
		subscriptions: make(map[string]*clientSubscriptionInfo),
		maxSubs:       maxSubs,
		logger:        logger,
		pool:          pool,
		sharedSubMgr:  sharedSubMgr,
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

	// Create subscriber for this subscription
	subscriber := &clientSubscriber{
		id:        subID,
		subType:   subType,
		params:    params,
		session:   cs,
		sendChan:  make(chan []byte, 100),
		closeChan: make(chan struct{}),
	}

	// Check if we have SharedSubscriptionManager
	if cs.sharedSubMgr != nil {
		// Subscribe through SharedSubscriptionManager
		if err := cs.sharedSubMgr.Subscribe(ctx, subType, params, subscriber); err != nil {
			return "", fmt.Errorf("failed to subscribe: %w", err)
		}
	} else {
		return "", fmt.Errorf("no shared subscription manager available")
	}

	// Store subscription info
	cs.mu.Lock()
	cs.subscriptions[subID] = &clientSubscriptionInfo{
		subID:      subID,
		subType:    subType,
		params:     params,
		subscriber: subscriber,
	}
	cs.mu.Unlock()

	// Start sender goroutine
	go cs.sendToClient(subscriber)

	cs.logger.Debug().
		Str("subID", subID).
		Str("type", string(subType)).
		Msg("subscription created via shared subscription manager")

	return subID, nil
}

// Unsubscribe removes a subscription
func (cs *ClientSession) Unsubscribe(subID string) error {
	cs.mu.Lock()
	subInfo, ok := cs.subscriptions[subID]
	if !ok {
		cs.mu.Unlock()
		return fmt.Errorf("subscription not found: %s", subID)
	}
	delete(cs.subscriptions, subID)
	cs.mu.Unlock()

	// Close the subscriber
	subInfo.subscriber.Close()

	// Unsubscribe from SharedSubscriptionManager
	if cs.sharedSubMgr != nil {
		if err := cs.sharedSubMgr.Unsubscribe(subInfo.subType, subInfo.params, subID); err != nil {
			cs.logger.Warn().Err(err).Str("subID", subID).Msg("failed to unsubscribe from shared subscription")
		}
	}

	cs.logger.Debug().Str("subID", subID).Msg("subscription removed")
	return nil
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

	subs := make([]*clientSubscriptionInfo, 0, len(cs.subscriptions))
	for _, sub := range cs.subscriptions {
		subs = append(subs, sub)
	}
	cs.subscriptions = make(map[string]*clientSubscriptionInfo)
	cs.mu.Unlock()

	// Close all subscriptions and unsubscribe from SharedSubscriptionManager
	for _, sub := range subs {
		sub.subscriber.Close()
		if cs.sharedSubMgr != nil {
			cs.sharedSubMgr.Unsubscribe(sub.subType, sub.params, sub.subID)
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

// sendToClient sends messages from the subscription to the client
func (cs *ClientSession) sendToClient(subscriber *clientSubscriber) {
	for {
		select {
		case data := <-subscriber.sendChan:
			cs.sendFunc(data)
		case <-subscriber.closeChan:
			return
		case <-cs.closeChan:
			return
		}
	}
}

// clientSubscriber implements Subscriber interface for a client subscription
type clientSubscriber struct {
	id        string
	subType   SubscriptionType
	params    json.RawMessage
	session   *ClientSession
	sendChan  chan []byte
	closeChan chan struct{}
	closed    bool
	mu        sync.Mutex
}

// ID implements Subscriber interface
func (s *clientSubscriber) ID() string {
	return s.id
}

// OnEvent implements Subscriber interface
func (s *clientSubscriber) OnEvent(event SubscriptionEvent) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// For newHeads from fallback upstreams, wait until main has the block
	// This prevents sending block notifications before main upstreams have the block,
	// which would cause RPC requests (routed to main) to fail with "block not found"
	if event.SubType == SubTypeNewHeads {
		// Check if this is from a fallback upstream
		u := s.session.pool.GetByName(event.UpstreamName)
		if u != nil && u.IsFallback() {
			blockNum, err := parseBlockNumber(event.Result)
			if err != nil {
				s.session.logger.Warn().Err(err).Msg("failed to parse block number from newHeads")
			} else {
				// Wait for main to have the block
				if !s.waitForMainBlock(blockNum) {
					// Session or subscriber is closing
					return
				}
			}
		}
	}

	// Create client notification with client's subscription ID
	clientNotification := NewNotification(s.id, event.Result)
	notifBytes, err := clientNotification.Bytes()
	if err != nil {
		s.session.logger.Warn().Err(err).Msg("failed to marshal notification")
		return
	}

	// Send to client via channel
	select {
	case s.sendChan <- notifBytes:
	case <-s.closeChan:
	case <-s.session.closeChan:
	default:
		// Channel full, drop message
		s.session.logger.Warn().Str("subID", s.id).Msg("send channel full, dropping event")
	}
}

// waitForMainBlock waits until at least one main upstream has the specified block
func (s *clientSubscriber) waitForMainBlock(blockNum uint64) bool {
	pool := s.session.pool

	// Check immediately - main has the block
	if pool.GetMainMaxBlock() >= blockNum {
		return true
	}

	// If no healthy main upstreams, allow fallback blocks through
	if !pool.HasHealthyMain() {
		return true
	}

	ticker := time.NewTicker(waitForMainBlockPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.closeChan:
			return false
		case <-s.session.closeChan:
			return false
		case <-ticker.C:
			// Main has the block now
			if pool.GetMainMaxBlock() >= blockNum {
				return true
			}
			// All main upstreams became unhealthy - allow fallback blocks
			if !pool.HasHealthyMain() {
				return true
			}
			// Main is still healthy but doesn't have block - keep waiting
		}
	}
}

// Close closes the subscriber
func (s *clientSubscriber) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.closeChan)
}

// parseBlockNumber extracts block number from newHeads result
func parseBlockNumber(result json.RawMessage) (uint64, error) {
	var header jsonrpc.BlockHeader
	if err := json.Unmarshal(result, &header); err != nil {
		return 0, err
	}
	return parseHexUint64(header.Number)
}

// parseHexUint64 parses a hex string (with 0x prefix) to uint64
func parseHexUint64(hex string) (uint64, error) {
	hex = strings.TrimPrefix(hex, "0x")
	return strconv.ParseUint(hex, 16, 64)
}

// generateSubID generates a unique subscription ID
func generateSubID() string {
	return fmt.Sprintf("0x%x", time.Now().UnixNano())
}
