package upstream

import (
	"sync"
	"time"
)

type cbState int

const (
	cbClosed cbState = iota
	cbOpen
	cbHalfOpen
)

// CircuitBreakerConfig holds circuit breaker configuration
type CircuitBreakerConfig struct {
	Enabled             bool
	FailureThreshold    int
	RecoveryTimeout     time.Duration
	HalfOpenMaxRequests int
}

// CircuitBreaker temporarily excludes an upstream from selection after consecutive failures
type CircuitBreaker struct {
	cfg             CircuitBreakerConfig
	state           cbState
	failures        int
	halfOpenSuccess int
	lastFailureAt   time.Time
	mu              sync.RWMutex
}

// NewCircuitBreaker creates a new CircuitBreaker
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.RecoveryTimeout <= 0 {
		cfg.RecoveryTimeout = 30 * time.Second
	}
	if cfg.HalfOpenMaxRequests <= 0 {
		cfg.HalfOpenMaxRequests = 2
	}
	return &CircuitBreaker{
		cfg:   cfg,
		state: cbClosed,
	}
}

// AllowRequest returns true if a request should be allowed
func (cb *CircuitBreaker) AllowRequest() bool {
	if !cb.cfg.Enabled {
		return true
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case cbClosed:
		return true
	case cbHalfOpen:
		return cb.halfOpenSuccess < cb.cfg.HalfOpenMaxRequests
	case cbOpen:
		if time.Since(cb.lastFailureAt) >= cb.cfg.RecoveryTimeout {
			cb.state = cbHalfOpen
			cb.halfOpenSuccess = 0
			return true
		}
		return false
	default:
		return true
	}
}

// RecordSuccess records a successful request
func (cb *CircuitBreaker) RecordSuccess() {
	if !cb.cfg.Enabled {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case cbHalfOpen:
		cb.halfOpenSuccess++
		if cb.halfOpenSuccess >= cb.cfg.HalfOpenMaxRequests {
			cb.state = cbClosed
			cb.failures = 0
		}
	case cbClosed:
		cb.failures = 0
	}
}

// RecordFailure records a failed request
func (cb *CircuitBreaker) RecordFailure() {
	if !cb.cfg.Enabled {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.lastFailureAt = time.Now()

	switch cb.state {
	case cbClosed:
		cb.failures++
		if cb.failures >= cb.cfg.FailureThreshold {
			cb.state = cbOpen
		}
	case cbHalfOpen:
		cb.state = cbOpen
		cb.halfOpenSuccess = 0
	}
}
