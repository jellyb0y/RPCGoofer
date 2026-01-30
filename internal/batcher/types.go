package batcher

import (
	"sync"
	"time"

	"rpcgofer/internal/config"
	"rpcgofer/internal/jsonrpc"
)

// BatchKey is the key for identifying a batch bucket
// Format: method:JSON(keyParams)
type BatchKey string

// BatchItem represents a single request in a batch
type BatchItem struct {
	ID           jsonrpc.ID             // client request ID
	Elements     []interface{}          // elements from aggregate param
	ElementCount int                    // number of elements this request contributed
	ResponseChan chan *jsonrpc.Response // channel to send response
}

// BatchBucket accumulates requests for a specific batch key
type BatchBucket struct {
	method     string
	keyParams  []interface{}          // all params except aggregate param
	config     *config.BatchMethodConfig
	items      []*BatchItem
	totalCount int                    // total number of aggregated elements
	timer      *time.Timer
	mu         sync.Mutex
	flushing   bool                   // prevents adding during flush
}

// NewBatchBucket creates a new batch bucket
func NewBatchBucket(method string, keyParams []interface{}, cfg *config.BatchMethodConfig) *BatchBucket {
	return &BatchBucket{
		method:    method,
		keyParams: keyParams,
		config:    cfg,
		items:     make([]*BatchItem, 0),
	}
}

// Add adds an item to the bucket
// Returns true if the bucket should be flushed (maxSize reached)
func (b *BatchBucket) Add(item *BatchItem) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.flushing {
		return false
	}

	b.items = append(b.items, item)
	b.totalCount += item.ElementCount

	return b.totalCount >= b.config.MaxSize
}

// StartTimer starts the flush timer if not already started
func (b *BatchBucket) StartTimer(onFlush func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.timer == nil && !b.flushing {
		b.timer = time.AfterFunc(b.config.GetMaxWaitDuration(), onFlush)
	}
}

// StopTimer stops the flush timer
func (b *BatchBucket) StopTimer() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

// TakeItems takes all items from the bucket for flushing
// Returns nil if already flushing or no items
func (b *BatchBucket) TakeItems() ([]*BatchItem, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.flushing || len(b.items) == 0 {
		return nil, 0
	}

	b.flushing = true
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}

	items := b.items
	totalCount := b.totalCount
	b.items = make([]*BatchItem, 0)
	b.totalCount = 0

	return items, totalCount
}

// Reset resets the bucket state after flush
func (b *BatchBucket) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushing = false
}

// IsEmpty returns true if bucket has no pending items
func (b *BatchBucket) IsEmpty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items) == 0 && !b.flushing
}

// GetMethod returns the method name
func (b *BatchBucket) GetMethod() string {
	return b.method
}

// GetKeyParams returns the key parameters
func (b *BatchBucket) GetKeyParams() []interface{} {
	return b.keyParams
}

// GetConfig returns the batch method config
func (b *BatchBucket) GetConfig() *config.BatchMethodConfig {
	return b.config
}
