package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"

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
