package upstream

import (
	"sync"
	"testing"
	"time"
)

// fakeClock provides a controllable now() for deterministic CB tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func makeCB(t *testing.T, cfg CircuitBreakerConfig) (*CircuitBreaker, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	cfg.Enabled = true
	cb := NewCircuitBreaker(cfg)
	cb.now = clock.Now
	return cb, clock
}

func TestCircuitBreakerClosedByDefault(t *testing.T) {
	cb, _ := makeCB(t, CircuitBreakerConfig{})
	if !cb.AllowRequest() {
		t.Fatalf("expected AllowRequest=true on fresh CB")
	}
}

func TestCircuitBreakerDisabledAlwaysAllows(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{Enabled: false, FailureThreshold: 1})
	for i := 0; i < 100; i++ {
		cb.RecordFailure()
	}
	if !cb.AllowRequest() {
		t.Fatalf("expected AllowRequest=true when CB is disabled")
	}
}

func TestCircuitBreakerAbsoluteThreshold(t *testing.T) {
	// Back-compat: 5 consecutive failures must open the CB regardless of rate config.
	cb, _ := makeCB(t, CircuitBreakerConfig{
		FailureThreshold:     5,
		WindowSize:           1 * time.Hour, // very wide so all events stay
		MinRequests:          1000,          // make rate trigger unreachable
		FailureRateThreshold: 0.99,
	})

	for i := 0; i < 4; i++ {
		cb.RecordFailure()
	}
	if !cb.AllowRequest() {
		t.Fatalf("CB should still be closed after 4 failures")
	}
	cb.RecordFailure()
	if cb.AllowRequest() {
		t.Fatalf("CB should be open after 5 failures (absolute threshold)")
	}
}

func TestCircuitBreakerSlidingWindowRate(t *testing.T) {
	// Absolute threshold 100 is unreachable — only the rate trigger can fire.
	// MinRequests=10 means rate-trigger requires at least 10 events in the window.
	cb, _ := makeCB(t, CircuitBreakerConfig{
		FailureThreshold:     100,
		WindowSize:           1 * time.Hour,
		MinRequests:          10,
		FailureRateThreshold: 0.5,
	})

	// Stage 1: 9 events (4F + 5S). total=9 < MinRequests → no rate trigger.
	for i := 0; i < 4; i++ {
		cb.RecordFailure()
	}
	for i := 0; i < 5; i++ {
		cb.RecordSuccess()
	}
	if !cb.AllowRequest() {
		t.Fatalf("CB should remain closed: only 9 events in window, MinRequests=10")
	}

	// Stage 2: one more failure → 10 events, 5F/10 = 50% >= 0.5 → open.
	cb.RecordFailure()
	if cb.AllowRequest() {
		t.Fatalf("CB should open: 5 failures / 10 events = 50%% >= 50%% threshold")
	}
}

func TestCircuitBreakerMinRequestsGate(t *testing.T) {
	// Below MinRequests, the rate trigger must NOT fire even at 100% failures.
	cb, _ := makeCB(t, CircuitBreakerConfig{
		FailureThreshold:     100,
		WindowSize:           1 * time.Hour,
		MinRequests:          10,
		FailureRateThreshold: 0.5,
	})
	for i := 0; i < 9; i++ {
		cb.RecordFailure()
	}
	if !cb.AllowRequest() {
		t.Fatalf("CB should remain closed: only 9 events, MinRequests=10")
	}
}

func TestCircuitBreakerWindowExpiry(t *testing.T) {
	// Old failures must drop out of the window and stop counting.
	cb, clock := makeCB(t, CircuitBreakerConfig{
		FailureThreshold:     5,
		WindowSize:           60 * time.Second,
		MinRequests:          100,
		FailureRateThreshold: 0.99,
	})
	// 4 failures inside window — closed.
	for i := 0; i < 4; i++ {
		cb.RecordFailure()
	}
	clock.Advance(2 * time.Minute) // expire all events
	if !cb.AllowRequest() {
		t.Fatalf("CB should remain closed: prior failures expired by window pruning")
	}
	// Now 4 fresh failures should also stay closed (still under threshold).
	for i := 0; i < 4; i++ {
		cb.RecordFailure()
	}
	if !cb.AllowRequest() {
		t.Fatalf("CB should remain closed after 4 fresh failures (threshold=5)")
	}
	cb.RecordFailure()
	if cb.AllowRequest() {
		t.Fatalf("CB should now be open with 5 fresh failures inside window")
	}
}

func TestCircuitBreakerRecoveryHalfOpenClose(t *testing.T) {
	cb, clock := makeCB(t, CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeout:     30 * time.Second,
		HalfOpenMaxRequests: 2,
		WindowSize:          1 * time.Minute,
		MinRequests:         100,
	})
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.AllowRequest() {
		t.Fatalf("CB should be open after 2 failures (threshold=2)")
	}

	// Before recovery — still open.
	clock.Advance(10 * time.Second)
	if cb.AllowRequest() {
		t.Fatalf("CB should still be open before RecoveryTimeout elapses")
	}

	// Recovery elapses — first AllowRequest transitions cbOpen → cbHalfOpen and returns true.
	clock.Advance(25 * time.Second)
	if !cb.AllowRequest() {
		t.Fatalf("CB should transition to half-open and allow probe after RecoveryTimeout")
	}

	// In half-open, two successes close the CB.
	cb.RecordSuccess()
	cb.RecordSuccess()
	if !cb.AllowRequest() {
		t.Fatalf("CB should be closed after HalfOpenMaxRequests successful probes")
	}
}

func TestCircuitBreakerHalfOpenFailReopens(t *testing.T) {
	cb, clock := makeCB(t, CircuitBreakerConfig{
		FailureThreshold:    1,
		RecoveryTimeout:     10 * time.Second,
		HalfOpenMaxRequests: 2,
		WindowSize:          1 * time.Minute,
	})
	cb.RecordFailure()
	clock.Advance(15 * time.Second)
	if !cb.AllowRequest() {
		t.Fatalf("CB should transition to half-open and allow probe")
	}
	// A single probe failure must NOT reopen the breaker (tolerate up to
	// HalfOpenMaxRequests-1 failures), to avoid open↔half-open flapping.
	cb.RecordFailure()
	if !cb.AllowRequest() {
		t.Fatalf("CB must stay half-open after a single probe failure (HalfOpenMaxRequests=2)")
	}
	// Reaching HalfOpenMaxRequests failed probes reopens the breaker.
	cb.RecordFailure()
	if cb.AllowRequest() {
		t.Fatalf("CB must reopen after HalfOpenMaxRequests failed probes")
	}
}

func TestCircuitBreakerOnStateChangeCallback(t *testing.T) {
	type transition struct{ from, to string }
	var (
		mu          sync.Mutex
		transitions []transition
	)
	cfg := CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeout:     5 * time.Second,
		HalfOpenMaxRequests: 1,
		WindowSize:          1 * time.Minute,
		Enabled:             true,
		OnStateChange: func(from, to string) {
			mu.Lock()
			transitions = append(transitions, transition{from, to})
			mu.Unlock()
		},
	}
	clock := newFakeClock()
	cb := NewCircuitBreaker(cfg)
	cb.now = clock.Now

	cb.RecordFailure()
	cb.RecordFailure() // closed → open
	clock.Advance(10 * time.Second)
	cb.AllowRequest() // open → half-open
	cb.RecordSuccess() // half-open → closed (HalfOpenMaxRequests=1)

	mu.Lock()
	defer mu.Unlock()
	want := []transition{
		{"closed", "open"},
		{"open", "half-open"},
		{"half-open", "closed"},
	}
	if len(transitions) != len(want) {
		t.Fatalf("transitions count: got %d want %d (%+v)", len(transitions), len(want), transitions)
	}
	for i, w := range want {
		if transitions[i] != w {
			t.Errorf("transition[%d]: got %+v want %+v", i, transitions[i], w)
		}
	}
}

func TestCircuitBreakerMaxEventsCap(t *testing.T) {
	// Fire more events than MaxEvents; verify the slice does not grow unbounded.
	cb, _ := makeCB(t, CircuitBreakerConfig{
		FailureThreshold:     100000,
		WindowSize:           1 * time.Hour,
		MinRequests:          100000,
		FailureRateThreshold: 0.99,
		MaxEvents:            100,
	})
	for i := 0; i < 1000; i++ {
		cb.RecordSuccess()
	}
	cb.mu.Lock()
	got := len(cb.events)
	cb.mu.Unlock()
	if got > 100 {
		t.Fatalf("events slice exceeded MaxEvents cap: got %d, want <= 100", got)
	}
}

func TestCircuitBreakerHalfOpenResetsEvents(t *testing.T) {
	// On half-open → closed transition, events from the previous failure cycle must be cleared
	// so they do not poison the freshly-recovered upstream's stats.
	cb, clock := makeCB(t, CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeout:     5 * time.Second,
		HalfOpenMaxRequests: 1,
		WindowSize:          1 * time.Hour,
		MinRequests:         100,
	})
	cb.RecordFailure()
	cb.RecordFailure() // open
	clock.Advance(10 * time.Second)
	if !cb.AllowRequest() {
		t.Fatalf("expected half-open allow")
	}
	cb.RecordSuccess() // closes
	cb.mu.Lock()
	got := len(cb.events)
	cb.mu.Unlock()
	if got != 0 {
		t.Fatalf("events should be cleared on half-open → closed transition, got len=%d", got)
	}
}
