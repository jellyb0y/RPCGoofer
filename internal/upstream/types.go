package upstream

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"rpcgofer/internal/config"
)

// BlockEventCallback is called when a new block is received from an upstream
type BlockEventCallback func(upstreamName string, result json.RawMessage)

// NewHeadsSubscriber represents a subscriber for newHeads events
type NewHeadsSubscriber interface {
	OnBlock(upstreamName string, result json.RawMessage)
	ID() string
}

// NewHeadsProvider provides newHeads subscription functionality
type NewHeadsProvider interface {
	SubscribeNewHeads(ctx context.Context, subscriber NewHeadsSubscriber) error
	UnsubscribeNewHeads(subscriberID string) error
}

// Role represents the upstream role
type Role string

const (
	RoleMain     Role = "main"
	RoleFallback Role = "fallback"
)

// RoleFromConfig converts config.Role to upstream.Role
func RoleFromConfig(r config.Role) Role {
	switch r {
	case config.RoleFallback:
		return RoleFallback
	default:
		return RoleMain
	}
}

// Status represents the health status of an upstream
type Status struct {
	healthy            atomic.Bool
	currentBlock       atomic.Uint64
	lastBlockTime      time.Time
	lastBlockTimeMu    sync.RWMutex
	requestCount       atomic.Uint64
	subscriptionCount  atomic.Int64
	subscriptionEvents atomic.Uint64
}

// NewStatus creates a new Status
func NewStatus() *Status {
	s := &Status{}
	s.healthy.Store(true)
	return s
}

// IsHealthy returns the health status
func (s *Status) IsHealthy() bool {
	return s.healthy.Load()
}

// SetHealthy sets the health status
func (s *Status) SetHealthy(healthy bool) {
	s.healthy.Store(healthy)
}

// GetCurrentBlock returns the current block number
func (s *Status) GetCurrentBlock() uint64 {
	return s.currentBlock.Load()
}

// SetCurrentBlock sets the current block number
func (s *Status) SetCurrentBlock(block uint64) {
	s.currentBlock.Store(block)
}

// GetLastBlockTime returns the time of the last block update
func (s *Status) GetLastBlockTime() time.Time {
	s.lastBlockTimeMu.RLock()
	defer s.lastBlockTimeMu.RUnlock()
	return s.lastBlockTime
}

// UpdateBlock updates the block if the new value is higher
// Returns true if the block was updated
func (s *Status) UpdateBlock(block uint64) bool {
	for {
		current := s.currentBlock.Load()
		if block <= current {
			return false
		}
		if s.currentBlock.CompareAndSwap(current, block) {
			s.lastBlockTimeMu.Lock()
			s.lastBlockTime = time.Now()
			s.lastBlockTimeMu.Unlock()
			return true
		}
	}
}

// IncrementRequestCount increments the request counter
func (s *Status) IncrementRequestCount() {
	s.requestCount.Add(1)
}

// IncrementRequestCountBy increments the request counter by a given value
func (s *Status) IncrementRequestCountBy(count uint64) {
	s.requestCount.Add(count)
}

// SwapRequestCount returns the current request count and resets it to zero
func (s *Status) SwapRequestCount() uint64 {
	return s.requestCount.Swap(0)
}

// IncrementSubscriptionCount increments the subscription counter
func (s *Status) IncrementSubscriptionCount() {
	s.subscriptionCount.Add(1)
}

// DecrementSubscriptionCount decrements the subscription counter
func (s *Status) DecrementSubscriptionCount() {
	s.subscriptionCount.Add(-1)
}

// GetSubscriptionCount returns the current subscription count
func (s *Status) GetSubscriptionCount() int64 {
	return s.subscriptionCount.Load()
}

// IncrementSubscriptionEvents increments the subscription events counter
func (s *Status) IncrementSubscriptionEvents() {
	s.subscriptionEvents.Add(1)
}

// SwapSubscriptionEvents returns the current subscription events count and resets it to zero
func (s *Status) SwapSubscriptionEvents() uint64 {
	return s.subscriptionEvents.Swap(0)
}

// MethodStats tracks method call statistics
type MethodStats struct {
	counts map[string]*atomic.Uint64
	mu     sync.RWMutex
}

// NewMethodStats creates a new MethodStats
func NewMethodStats() *MethodStats {
	return &MethodStats{
		counts: make(map[string]*atomic.Uint64),
	}
}

// Increment increments the count for a method
func (m *MethodStats) Increment(method string) {
	m.mu.RLock()
	counter, exists := m.counts[method]
	m.mu.RUnlock()

	if exists {
		counter.Add(1)
		return
	}

	m.mu.Lock()
	// Double check after acquiring write lock
	if counter, exists = m.counts[method]; exists {
		m.mu.Unlock()
		counter.Add(1)
		return
	}
	counter = &atomic.Uint64{}
	counter.Store(1)
	m.counts[method] = counter
	m.mu.Unlock()
}

// IncrementBy increments the count for a method by a given value
func (m *MethodStats) IncrementBy(method string, count uint64) {
	m.mu.RLock()
	counter, exists := m.counts[method]
	m.mu.RUnlock()

	if exists {
		counter.Add(count)
		return
	}

	m.mu.Lock()
	// Double check after acquiring write lock
	if counter, exists = m.counts[method]; exists {
		m.mu.Unlock()
		counter.Add(count)
		return
	}
	counter = &atomic.Uint64{}
	counter.Store(count)
	m.counts[method] = counter
	m.mu.Unlock()
}

// MethodCount represents a method and its count
type MethodCount struct {
	Method string
	Count  uint64
}

// SwapAndGetTop returns top N methods by count and resets all counters
func (m *MethodStats) SwapAndGetTop(n int) []MethodCount {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]MethodCount, 0, len(m.counts))
	for method, counter := range m.counts {
		count := counter.Swap(0)
		if count > 0 {
			result = append(result, MethodCount{Method: method, Count: count})
		}
	}

	// Sort by count descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	// Return top N
	if len(result) > n {
		result = result[:n]
	}

	return result
}
