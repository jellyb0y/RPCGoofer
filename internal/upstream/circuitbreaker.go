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

func (s cbState) String() string {
	switch s {
	case cbClosed:
		return "closed"
	case cbOpen:
		return "open"
	case cbHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig holds circuit breaker configuration.
//
// CB opens when EITHER:
//   - failures in the sliding window >= FailureThreshold (absolute trigger; back-compat with old behaviour)
//   - total events in window >= MinRequests AND failures/total >= FailureRateThreshold (rate trigger)
//
// OnStateChange is invoked AFTER the internal lock is released, with copies of state names.
// It is guaranteed to fire in the same order as state transitions occurred.
//
// Re-entrancy is FORBIDDEN: the callback MUST NOT invoke any CircuitBreaker methods on the
// same instance, otherwise a deadlock or stack overflow may occur. Keep the callback fast
// (logging / metrics only).
type CircuitBreakerConfig struct {
	Enabled              bool
	FailureThreshold     int
	RecoveryTimeout      time.Duration
	HalfOpenMaxRequests  int
	WindowSize           time.Duration       // sliding window size
	MinRequests          int                 // minimum events in window before rate-trigger applies
	FailureRateThreshold float64             // 0..1; open when failures/total >= this
	MaxEvents            int                 // hard cap on the events slice; oldest entries are dropped first
	OnStateChange        func(from, to string)
}

type cbEvent struct {
	at      time.Time
	success bool
}

// CircuitBreaker temporarily excludes an upstream from selection after enough failures
// accumulate within a sliding window.
type CircuitBreaker struct {
	cfg             CircuitBreakerConfig
	state           cbState
	events          []cbEvent
	halfOpenSuccess int
	halfOpenFailure int
	lastFailureAt   time.Time
	mu              sync.Mutex
	// now is injectable for tests; production code uses time.Now.
	now func() time.Time
}

// NewCircuitBreaker creates a new CircuitBreaker.
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
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 60 * time.Second
	}
	if cfg.MinRequests <= 0 {
		cfg.MinRequests = 10
	}
	if cfg.FailureRateThreshold <= 0 {
		cfg.FailureRateThreshold = 0.5
	}
	if cfg.MaxEvents <= 0 {
		cfg.MaxEvents = 10000
	}
	return &CircuitBreaker{
		cfg:   cfg,
		state: cbClosed,
		now:   time.Now,
	}
}

// pruneAndCount drops events older than WindowSize and trims to MaxEvents from the head.
// Returns (totalInWindow, failuresInWindow). Caller must hold cb.mu.
func (cb *CircuitBreaker) pruneAndCount() (int, int) {
	cutoff := cb.now().Add(-cb.cfg.WindowSize)
	// Drop expired events from the head.
	i := 0
	for i < len(cb.events) && cb.events[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		cb.events = cb.events[i:]
	}
	// Hard cap: drop oldest if exceeds MaxEvents.
	if len(cb.events) > cb.cfg.MaxEvents {
		cb.events = cb.events[len(cb.events)-cb.cfg.MaxEvents:]
	}
	failures := 0
	for _, e := range cb.events {
		if !e.success {
			failures++
		}
	}
	return len(cb.events), failures
}

// AllowRequest returns true if a request should be allowed.
// Performs a state transition cbOpen -> cbHalfOpen if the recovery timeout has elapsed.
func (cb *CircuitBreaker) AllowRequest() bool {
	if !cb.cfg.Enabled {
		return true
	}

	var (
		changed bool
		fromStr string
		toStr   string
		allowed bool
	)

	cb.mu.Lock()
	switch cb.state {
	case cbClosed:
		allowed = true
	case cbHalfOpen:
		// Allow probes until either enough successes close the breaker or enough
		// failures reopen it. Gating on both counters lets a few probes fail without
		// instantly slamming the breaker back open (avoids open↔half-open flapping).
		allowed = cb.halfOpenSuccess < cb.cfg.HalfOpenMaxRequests &&
			cb.halfOpenFailure < cb.cfg.HalfOpenMaxRequests
	case cbOpen:
		if cb.now().Sub(cb.lastFailureAt) >= cb.cfg.RecoveryTimeout {
			fromStr = cb.state.String()
			cb.state = cbHalfOpen
			cb.halfOpenSuccess = 0
			cb.halfOpenFailure = 0
			toStr = cb.state.String()
			changed = true
			allowed = true
		} else {
			allowed = false
		}
	default:
		allowed = true
	}
	cb.mu.Unlock()

	if changed && cb.cfg.OnStateChange != nil {
		cb.cfg.OnStateChange(fromStr, toStr)
	}
	return allowed
}

// RecordSuccess records a successful request.
func (cb *CircuitBreaker) RecordSuccess() {
	if !cb.cfg.Enabled {
		return
	}

	var (
		changed bool
		fromStr string
		toStr   string
	)

	cb.mu.Lock()
	cb.events = append(cb.events, cbEvent{at: cb.now(), success: true})
	cb.pruneAndCount()

	if cb.state == cbHalfOpen {
		cb.halfOpenSuccess++
		if cb.halfOpenSuccess >= cb.cfg.HalfOpenMaxRequests {
			fromStr = cb.state.String()
			cb.state = cbClosed
			cb.halfOpenFailure = 0
			cb.events = cb.events[:0] // reset stats for the new cycle
			toStr = cb.state.String()
			changed = true
		}
	}
	cb.mu.Unlock()

	if changed && cb.cfg.OnStateChange != nil {
		cb.cfg.OnStateChange(fromStr, toStr)
	}
}

// RecordFailure records a failed request and may transition state to cbOpen.
func (cb *CircuitBreaker) RecordFailure() {
	if !cb.cfg.Enabled {
		return
	}

	var (
		changed bool
		fromStr string
		toStr   string
	)

	cb.mu.Lock()
	now := cb.now()
	cb.events = append(cb.events, cbEvent{at: now, success: false})
	total, failures := cb.pruneAndCount()
	cb.lastFailureAt = now

	switch cb.state {
	case cbClosed:
		hitAbsolute := failures >= cb.cfg.FailureThreshold
		hitRate := total >= cb.cfg.MinRequests &&
			cb.cfg.FailureRateThreshold > 0 &&
			float64(failures)/float64(total) >= cb.cfg.FailureRateThreshold
		if hitAbsolute || hitRate {
			fromStr = cb.state.String()
			cb.state = cbOpen
			toStr = cb.state.String()
			changed = true
		}
	case cbHalfOpen:
		// Reopen only once enough probes have failed, not on the first failure —
		// a single slow/failed probe shouldn't blackhole an otherwise-recovering upstream.
		cb.halfOpenFailure++
		if cb.halfOpenFailure >= cb.cfg.HalfOpenMaxRequests {
			fromStr = cb.state.String()
			cb.state = cbOpen
			cb.halfOpenSuccess = 0
			cb.halfOpenFailure = 0
			toStr = cb.state.String()
			changed = true
		}
	}
	cb.mu.Unlock()

	if changed && cb.cfg.OnStateChange != nil {
		cb.cfg.OnStateChange(fromStr, toStr)
	}
}
