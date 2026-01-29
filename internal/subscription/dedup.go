package subscription

import (
	"encoding/json"
	"fmt"

	lru "github.com/hashicorp/golang-lru/v2"

	"rpcgofer/internal/jsonrpc"
)

// Deduplicator handles deduplication of subscription events
type Deduplicator struct {
	cache *lru.Cache[string, bool]
}

// NewDeduplicator creates a new Deduplicator with the given cache size
func NewDeduplicator(size int) (*Deduplicator, error) {
	cache, err := lru.New[string, bool](size)
	if err != nil {
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}
	return &Deduplicator{cache: cache}, nil
}

// IsDuplicate checks if an event has been seen before
// Returns true if it's a duplicate, false if it's new
func (d *Deduplicator) IsDuplicate(subType SubscriptionType, result json.RawMessage) bool {
	key := d.generateKey(subType, result)
	if key == "" {
		// Can't generate key, treat as not duplicate
		return false
	}

	// Check if we've seen this key
	if d.cache.Contains(key) {
		return true
	}

	// Add to cache
	d.cache.Add(key, true)
	return false
}

// generateKey generates a unique key for deduplication based on subscription type
func (d *Deduplicator) generateKey(subType SubscriptionType, result json.RawMessage) string {
	switch subType {
	case SubTypeNewHeads:
		return d.keyForNewHeads(result)
	case SubTypeLogs:
		return d.keyForLogs(result)
	case SubTypeNewPendingTransactions:
		return d.keyForPendingTx(result)
	default:
		// For unknown types, use hash of the entire result
		return fmt.Sprintf("unknown:%x", hashBytes(result))
	}
}

// keyForNewHeads generates a key for newHeads events using blockHash
func (d *Deduplicator) keyForNewHeads(result json.RawMessage) string {
	var header jsonrpc.BlockHeader
	if err := json.Unmarshal(result, &header); err != nil {
		return ""
	}
	if header.Hash == "" {
		return ""
	}
	return fmt.Sprintf("block:%s", header.Hash)
}

// keyForLogs generates a key for logs events using blockHash:txIndex:logIndex
func (d *Deduplicator) keyForLogs(result json.RawMessage) string {
	var log jsonrpc.Log
	if err := json.Unmarshal(result, &log); err != nil {
		return ""
	}
	if log.BlockHash == "" || log.TransactionIndex == "" || log.LogIndex == "" {
		return ""
	}
	return fmt.Sprintf("log:%s:%s:%s", log.BlockHash, log.TransactionIndex, log.LogIndex)
}

// keyForPendingTx generates a key for pending transaction events using txHash
func (d *Deduplicator) keyForPendingTx(result json.RawMessage) string {
	var txHash string
	if err := json.Unmarshal(result, &txHash); err != nil {
		return ""
	}
	if txHash == "" {
		return ""
	}
	return fmt.Sprintf("tx:%s", txHash)
}

// Clear clears the deduplication cache
func (d *Deduplicator) Clear() {
	d.cache.Purge()
}

// Len returns the current cache size
func (d *Deduplicator) Len() int {
	return d.cache.Len()
}

// hashBytes creates a simple hash of bytes for deduplication
func hashBytes(data []byte) uint64 {
	var hash uint64 = 14695981039346656037 // FNV-1a offset basis
	for _, b := range data {
		hash ^= uint64(b)
		hash *= 1099511628211 // FNV-1a prime
	}
	return hash
}
