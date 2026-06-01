package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
	"rpcgofer/internal/jsonrpc"
)

func newTestUpstream(t *testing.T, cfg Config) *Upstream {
	t.Helper()
	cfg.Logger = zerolog.Nop()
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	if cfg.Weight == 0 {
		cfg.Weight = 1
	}
	if cfg.Role == "" {
		cfg.Role = RoleMain
	}
	// Set high thresholds so absolute trigger never fires — keeps tests focused on
	// per-call RecordFailure/RecordSuccess accounting via cbStats.
	cfg.CircuitBreakerCfg = CircuitBreakerConfig{
		Enabled:              true,
		FailureThreshold:     1000,
		RecoveryTimeout:      time.Hour,
		HalfOpenMaxRequests:  1,
		WindowSize:           time.Hour,
		MinRequests:          1000,
		FailureRateThreshold: 0.99,
		MaxEvents:            1000,
	}
	return NewUpstream(cfg)
}

// cbStats reaches into the circuit breaker to count events for assertions.
func cbStats(cb *CircuitBreaker) (total, failures int) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	for _, e := range cb.events {
		total++
		if !e.success {
			failures++
		}
	}
	return total, failures
}

func TestUpstream_GetHistoricalRange_Default(t *testing.T) {
	u := newTestUpstream(t, Config{Name: "u"})
	if got := u.GetHistoricalRange(); got != 0 {
		t.Fatalf("default historical range: got %d want 0", got)
	}
}

func TestUpstream_GetHistoricalRange_Custom(t *testing.T) {
	u := newTestUpstream(t, Config{Name: "u", HistoricalBlockRange: 128})
	if got := u.GetHistoricalRange(); got != 128 {
		t.Fatalf("historical range: got %d want 128", got)
	}
}

func TestUpstream_RecordsSuccessOnJSONRPCError(t *testing.T) {
	// HTTP 200 + JSON-RPC error in body must NOT trip CB — it's a logical error,
	// the upstream itself is alive and responding.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"historical state is not available"}}`))
	}))
	defer server.Close()

	u := newTestUpstream(t, Config{Name: "u", RPCURL: server.URL})
	req, err := jsonrpc.NewRequest("debug_traceBlockByNumber", nil, jsonrpc.NewIDInt(1))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	for i := 0; i < 10; i++ {
		resp, err := u.ExecuteHTTP(context.Background(), req)
		if err != nil {
			t.Fatalf("ExecuteHTTP returned transport error: %v", err)
		}
		if !resp.HasError() {
			t.Fatalf("expected JSON-RPC error in response")
		}
	}

	total, failures := cbStats(u.circuitBreaker)
	if failures != 0 {
		t.Fatalf("CB recorded %d failures on JSON-RPC errors; expected 0", failures)
	}
	if total != 10 {
		t.Fatalf("CB recorded %d total events; expected 10 (all successes)", total)
	}
}

func TestUpstream_RecordsFailureOnHTTP500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`upstream overloaded`))
	}))
	defer server.Close()

	u := newTestUpstream(t, Config{Name: "u", RPCURL: server.URL})
	req, err := jsonrpc.NewRequest("eth_call", nil, jsonrpc.NewIDInt(1))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	if _, err := u.ExecuteHTTP(context.Background(), req); err == nil {
		t.Fatalf("expected error on HTTP 500")
	}
	total, failures := cbStats(u.circuitBreaker)
	if failures != 1 || total != 1 {
		t.Fatalf("expected 1 failure recorded; got total=%d failures=%d", total, failures)
	}
}

func TestUpstream_ExecuteIgnoringCB_BypassesOpenBreaker(t *testing.T) {
	// A healthy, reachable upstream whose circuit breaker is OPEN must still be
	// usable via the last-resort bypass (ExecuteIgnoringCB), while the gated Execute
	// is rejected. This is what prevents a full group outage when the sole live
	// upstream's breaker has tripped.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer server.Close()

	u := newTestUpstream(t, Config{Name: "u", RPCURL: server.URL})
	// Force the breaker open.
	u.circuitBreaker.cfg.FailureThreshold = 1
	u.circuitBreaker.RecordFailure()
	if u.AllowRequest() {
		t.Fatalf("precondition: breaker should be open")
	}

	req, err := jsonrpc.NewRequest("eth_blockNumber", nil, jsonrpc.NewIDInt(1))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	// Gated Execute is blocked by the open breaker.
	if _, err := u.Execute(context.Background(), req); err == nil {
		t.Fatalf("Execute should be blocked by the open breaker")
	}
	// Bypass reaches the healthy upstream and succeeds.
	resp, err := u.ExecuteIgnoringCB(context.Background(), req)
	if err != nil {
		t.Fatalf("ExecuteIgnoringCB returned error: %v", err)
	}
	if resp == nil || resp.HasError() {
		t.Fatalf("expected a successful response via the bypass")
	}
}

func TestPool_GetForRequestIgnoringCB(t *testing.T) {
	groupCfg := config.GroupConfig{Name: "test", Upstreams: []config.UpstreamConfig{
		{Name: "main1", RPCURL: "http://127.0.0.1:1/", Weight: 1, Role: config.RoleMain},
		{Name: "fb1", RPCURL: "http://127.0.0.1:1/", Weight: 1, Role: config.RoleFallback},
	}}
	globalCfg := &config.Config{
		RequestTimeout:                     5000,
		CircuitBreakerEnabled:              true,
		CircuitBreakerFailureThreshold:     1,
		CircuitBreakerRecoveryTimeout:      3600000,
		CircuitBreakerHalfOpenRequests:     1,
		CircuitBreakerWindowSize:           3600000,
		CircuitBreakerMinRequests:          1000,
		CircuitBreakerFailureRateThreshold: 0.99,
		CircuitBreakerMaxEvents:            1000,
	}
	pool := NewPool(groupCfg, globalCfg, zerolog.Nop())
	for _, u := range pool.GetAll() {
		u.SetHealthy(true)
		u.SetCurrentBlock(1000)
	}
	// Open the main upstream's breaker.
	main := pool.GetByName("main1")
	main.circuitBreaker.RecordFailure()
	if main.AllowRequest() {
		t.Fatalf("precondition: main breaker should be open")
	}

	// Normal selection drops the CB-open main and falls back to the fallback.
	forReq := pool.GetForRequest()
	if len(forReq) != 1 || forReq[0].Name() != "fb1" {
		t.Fatalf("GetForRequest should return only the fallback while main CB is open; got %d upstreams", len(forReq))
	}
	// Ignoring the breaker, the block-healthy main is returned (and main is preferred over fallback).
	ignoring := pool.GetForRequestIgnoringCB()
	if len(ignoring) != 1 || ignoring[0].Name() != "main1" {
		t.Fatalf("GetForRequestIgnoringCB should return the block-healthy main ignoring CB; got %d upstreams", len(ignoring))
	}
}

func TestUpstream_RecordsFailureOnTransportError(t *testing.T) {
	// Point at an unreachable port to trigger a transport error from net/http.
	u := newTestUpstream(t, Config{Name: "u", RPCURL: "http://127.0.0.1:1/"})
	req, err := jsonrpc.NewRequest("eth_blockNumber", nil, jsonrpc.NewIDInt(1))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := u.ExecuteHTTP(ctx, req); err == nil {
		t.Fatalf("expected transport error")
	}
	_, failures := cbStats(u.circuitBreaker)
	if failures != 1 {
		t.Fatalf("expected 1 failure on transport error; got %d", failures)
	}
}
