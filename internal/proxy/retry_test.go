package proxy

import (
	"testing"

	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
	"rpcgofer/internal/upstream"
)

// buildTestPool creates a pool with the given upstream specs and applies block/health.
// Each upstream gets currentBlock = currentBlocks[i] and is marked healthy.
type upSpec struct {
	name        string
	role        config.Role
	historical  uint64
	curBlock    uint64
	healthy     bool
	rpcURL      string
}

func buildTestPool(t *testing.T, specs []upSpec) *upstream.Pool {
	t.Helper()
	upstreams := make([]config.UpstreamConfig, 0, len(specs))
	for _, s := range specs {
		rpcURL := s.rpcURL
		if rpcURL == "" {
			rpcURL = "http://127.0.0.1:1/" // unreachable; tests never actually dispatch.
		}
		upstreams = append(upstreams, config.UpstreamConfig{
			Name:                 s.name,
			RPCURL:               rpcURL,
			Weight:               1,
			Role:                 s.role,
			HistoricalBlockRange: s.historical,
		})
	}
	groupCfg := config.GroupConfig{Name: "test", Upstreams: upstreams}
	globalCfg := &config.Config{
		RequestTimeout:                     5000,
		HealthCheckInterval:                10000,
		StatusLogInterval:                  5000,
		StatsLogInterval:                   60000,
		LagRecoveryTimeout:                 2000,
		UpstreamMessageTimeout:             60000,
		UpstreamReconnectInterval:          5000,
		CircuitBreakerEnabled:              true,
		CircuitBreakerFailureThreshold:     5,
		CircuitBreakerRecoveryTimeout:      30000,
		CircuitBreakerHalfOpenRequests:     2,
		CircuitBreakerWindowSize:           60000,
		CircuitBreakerMinRequests:          10,
		CircuitBreakerFailureRateThreshold: 0.5,
		CircuitBreakerMaxEvents:            10000,
		UpstreamRequestTimeout:             15000,
	}
	pool := upstream.NewPool(groupCfg, globalCfg, zerolog.Nop())
	// Set runtime state per spec — pool.GetForRequest() filters by IsHealthy + IsMain + AllowRequest.
	for i, u := range pool.GetAll() {
		u.SetCurrentBlock(specs[i].curBlock)
		u.SetHealthy(specs[i].healthy)
	}
	return pool
}

func TestApplyHistoricalAndLagExclude_ExcludesPrunedNode(t *testing.T) {
	pool := buildTestPool(t, []upSpec{
		{name: "fullnode", role: config.RoleMain, historical: 128, curBlock: 10000, healthy: true},
		{name: "archive", role: config.RoleMain, historical: 0, curBlock: 10000, healthy: true},
	})

	// Old block (10000 - 5000 = 5000), well outside fullnode's 128-block window.
	got := applyHistoricalAndLagExclude(pool, 5000, 5000, nil)

	if !got["fullnode"] {
		t.Errorf("expected 'fullnode' to be excluded for block 5000 (range 128, head 10000)")
	}
	if got["archive"] {
		t.Errorf("expected 'archive' (range=0) NOT to be excluded")
	}
}

func TestApplyHistoricalAndLagExclude_KeepsNodeWithinWindow(t *testing.T) {
	pool := buildTestPool(t, []upSpec{
		{name: "fullnode", role: config.RoleMain, historical: 128, curBlock: 10000, healthy: true},
	})

	// Block 9950: 9950 + 128 = 10078 > 10000 → still inside window.
	got := applyHistoricalAndLagExclude(pool, 9950, 9950, nil)

	if got["fullnode"] {
		t.Errorf("expected 'fullnode' to remain available for block 9950 (within 128-block window)")
	}
}

func TestApplyHistoricalAndLagExclude_LagBehindHead(t *testing.T) {
	pool := buildTestPool(t, []upSpec{
		{name: "lagging", role: config.RoleMain, historical: 0, curBlock: 9000, healthy: true},
	})

	// requestedBlock=9500 > lagging.currentBlock=9000 → exclude (block-lag).
	got := applyHistoricalAndLagExclude(pool, 9500, 9500, nil)

	if !got["lagging"] {
		t.Errorf("expected 'lagging' to be excluded — currentBlock=9000 < requestedBlock=9500")
	}
}

func TestApplyHistoricalAndLagExclude_BatchUsesMinBlock(t *testing.T) {
	// Batch semantics: an upstream must serve EVERY block in the batch, so the OLDEST
	// requested block is the strict bound. Even if the newest block is in-window, the
	// oldest one being out-of-window must still exclude the upstream.
	pool := buildTestPool(t, []upSpec{
		{name: "fullnode", role: config.RoleMain, historical: 128, curBlock: 10000, healthy: true},
	})

	// maxBlock=9990 (in-window), minBlock=5000 (out-of-window).
	got := applyHistoricalAndLagExclude(pool, 9990, 5000, nil)

	if !got["fullnode"] {
		t.Errorf("expected 'fullnode' to be excluded for batch [5000..9990]: oldest block 5000 is outside the 128-block window")
	}
}

func TestApplyHistoricalAndLagExclude_NoUnderflow(t *testing.T) {
	// When historicalBlockRange > currentBlock (e.g. node still syncing), the underflow
	// guard must keep the node available — better try than drop a fresh node.
	pool := buildTestPool(t, []upSpec{
		{name: "syncing", role: config.RoleMain, historical: 1000000, curBlock: 100, healthy: true},
	})

	got := applyHistoricalAndLagExclude(pool, 50, 50, nil)

	if got["syncing"] {
		t.Errorf("guard against uint64 underflow failed — 'syncing' should not be excluded when range > currentBlock")
	}
}

func TestApplyHistoricalAndLagExclude_ZeroRequestedBlockSkips(t *testing.T) {
	// Methods without a block parameter (requestedBlock=0) must not trigger any exclude.
	pool := buildTestPool(t, []upSpec{
		{name: "fullnode", role: config.RoleMain, historical: 128, curBlock: 10000, healthy: true},
	})

	got := applyHistoricalAndLagExclude(pool, 0, 0, nil)

	if got != nil && got["fullnode"] {
		t.Errorf("expected no exclusion when requestedBlock is 0 (non-block method)")
	}
}
