package upstream

import (
	"rpcgofer/internal/config"
	"sync/atomic"
)

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
	healthy      atomic.Bool
	currentBlock atomic.Uint64
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

// UpdateBlock updates the block if the new value is higher
// Returns true if the block was updated
func (s *Status) UpdateBlock(block uint64) bool {
	for {
		current := s.currentBlock.Load()
		if block <= current {
			return false
		}
		if s.currentBlock.CompareAndSwap(current, block) {
			return true
		}
	}
}
