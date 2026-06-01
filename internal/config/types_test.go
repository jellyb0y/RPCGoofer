package config

import (
	"encoding/json"
	"testing"
)

func TestGroupConfig_UnmarshalWithoutPluginParams(t *testing.T) {
	data := `{"name": "base", "upstreams": []}`
	var gc GroupConfig
	if err := json.Unmarshal([]byte(data), &gc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if gc.Name != "base" {
		t.Errorf("name = %q, want %q", gc.Name, "base")
	}
	if gc.PluginParams != nil {
		t.Errorf("PluginParams should be nil, got %v", gc.PluginParams)
	}
}

func TestGroupConfig_UnmarshalWithPluginParams(t *testing.T) {
	data := `{"name": "bsc", "upstreams": [], "pluginParams": {"codeCheckerAddress": "0xABC"}}`
	var gc GroupConfig
	if err := json.Unmarshal([]byte(data), &gc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if gc.Name != "bsc" {
		t.Errorf("name = %q, want %q", gc.Name, "bsc")
	}
	if gc.PluginParams == nil {
		t.Fatal("PluginParams should not be nil")
	}
	addr, ok := gc.PluginParams["codeCheckerAddress"].(string)
	if !ok || addr != "0xABC" {
		t.Errorf("codeCheckerAddress = %v, want %q", gc.PluginParams["codeCheckerAddress"], "0xABC")
	}
}

func TestUpstreamConfig_UnmarshalHistoricalBlockRange(t *testing.T) {
	data := `{"name":"u","rpcUrl":"http://x","weight":10,"role":"main","historicalBlockRange":128}`
	var uc UpstreamConfig
	if err := json.Unmarshal([]byte(data), &uc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if uc.HistoricalBlockRange != 128 {
		t.Errorf("HistoricalBlockRange = %d, want 128", uc.HistoricalBlockRange)
	}
}

func TestUpstreamConfig_HistoricalBlockRangeDefaultsToZero(t *testing.T) {
	data := `{"name":"u","rpcUrl":"http://x","weight":10,"role":"main"}`
	var uc UpstreamConfig
	if err := json.Unmarshal([]byte(data), &uc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if uc.HistoricalBlockRange != 0 {
		t.Errorf("HistoricalBlockRange = %d when omitted, want 0 (unlimited/archive)", uc.HistoricalBlockRange)
	}
}

func TestConfig_NewSlidingWindowFieldsParse(t *testing.T) {
	data := `{
		"groups":[],
		"circuitBreakerWindowSize": 30000,
		"circuitBreakerMinRequests": 5,
		"circuitBreakerFailureRateThreshold": 0.7,
		"circuitBreakerMaxEvents": 500,
		"upstreamRequestTimeout": 10000
	}`
	var c Config
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if c.CircuitBreakerWindowSize != 30000 {
		t.Errorf("CircuitBreakerWindowSize = %d, want 30000", c.CircuitBreakerWindowSize)
	}
	if c.CircuitBreakerMinRequests != 5 {
		t.Errorf("CircuitBreakerMinRequests = %d, want 5", c.CircuitBreakerMinRequests)
	}
	if c.CircuitBreakerFailureRateThreshold != 0.7 {
		t.Errorf("CircuitBreakerFailureRateThreshold = %v, want 0.7", c.CircuitBreakerFailureRateThreshold)
	}
	if c.CircuitBreakerMaxEvents != 500 {
		t.Errorf("CircuitBreakerMaxEvents = %d, want 500", c.CircuitBreakerMaxEvents)
	}
	if c.UpstreamRequestTimeout != 10000 {
		t.Errorf("UpstreamRequestTimeout = %d, want 10000", c.UpstreamRequestTimeout)
	}
}

func TestConfig_GetUpstreamRequestTimeoutDuration_DefaultsWhenZero(t *testing.T) {
	c := &Config{}
	got := c.GetUpstreamRequestTimeoutDuration()
	want := DefaultUpstreamRequestTimeout
	if int(got.Milliseconds()) != want {
		t.Errorf("GetUpstreamRequestTimeoutDuration default = %dms, want %dms", got.Milliseconds(), want)
	}
}

func TestConfig_GetCircuitBreakerWindowSizeDuration_DefaultsWhenZero(t *testing.T) {
	c := &Config{}
	got := c.GetCircuitBreakerWindowSizeDuration()
	want := DefaultCircuitBreakerWindowSize
	if int(got.Milliseconds()) != want {
		t.Errorf("GetCircuitBreakerWindowSizeDuration default = %dms, want %dms", got.Milliseconds(), want)
	}
}

func TestConfig_GetResponseHeaderTimeoutDuration_DefaultsWhenZero(t *testing.T) {
	c := &Config{}
	got := c.GetResponseHeaderTimeoutDuration()
	if int(got.Milliseconds()) != DefaultResponseHeaderTimeout {
		t.Errorf("GetResponseHeaderTimeoutDuration default = %dms, want %dms", got.Milliseconds(), DefaultResponseHeaderTimeout)
	}
	c2 := &Config{ResponseHeaderTimeout: 7000}
	if got := c2.GetResponseHeaderTimeoutDuration(); got.Milliseconds() != 7000 {
		t.Errorf("GetResponseHeaderTimeoutDuration explicit = %dms, want 7000ms", got.Milliseconds())
	}
}

func TestConfig_GetIdleConnTimeoutDuration_DefaultsWhenZero(t *testing.T) {
	c := &Config{}
	got := c.GetIdleConnTimeoutDuration()
	if int(got.Milliseconds()) != DefaultIdleConnTimeout {
		t.Errorf("GetIdleConnTimeoutDuration default = %dms, want %dms", got.Milliseconds(), DefaultIdleConnTimeout)
	}
	c2 := &Config{IdleConnTimeout: 45000}
	if got := c2.GetIdleConnTimeoutDuration(); got.Milliseconds() != 45000 {
		t.Errorf("GetIdleConnTimeoutDuration explicit = %dms, want 45000ms", got.Milliseconds())
	}
}

func TestGroupConfig_MarshalWithoutPluginParams(t *testing.T) {
	gc := GroupConfig{Name: "base", Upstreams: []UpstreamConfig{}}
	data, err := json.Marshal(gc)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	// pluginParams should be omitted
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	if _, ok := m["pluginParams"]; ok {
		t.Error("pluginParams should be omitted when nil")
	}
}
