package config

import "time"

// Role defines the upstream role type
type Role string

const (
	RoleMain     Role = "main"
	RoleFallback Role = "fallback"
)

// Config represents the main configuration structure
type Config struct {
	Host           string `json:"host"`
	RPCPort        int    `json:"rpcPort"`
	WSPort         int    `json:"wsPort"`
	LogLevel       string `json:"logLevel"`
	MaxBodySize    int64  `json:"maxBodySize"`
	RequestTimeout int    `json:"requestTimeout"`
	// ResponseHeaderTimeout bounds how long the HTTP client waits for an upstream to START
	// responding (response headers). Without it, a stale/dead pooled connection makes a request
	// hang until RequestTimeout. ms; default 10000.
	ResponseHeaderTimeout int `json:"responseHeaderTimeout"`
	// IdleConnTimeout is how long an idle keep-alive connection to an upstream is kept before
	// being closed. Keep it BELOW the upstream/LB server-side idle timeout (~60s for Cloudflare),
	// otherwise gofer reuses a connection the server already dropped and the next request hangs.
	// ms; default 30000.
	IdleConnTimeout                int    `json:"idleConnTimeout"`
	HealthCheckInterval            int    `json:"healthCheckInterval"`
	StatusLogInterval              int    `json:"statusLogInterval"`
	StatsLogInterval               int    `json:"statsLogInterval"`
	BlockLagThreshold              uint64 `json:"blockLagThreshold"`
	LagRecoveryTimeout             int    `json:"lagRecoveryTimeout"`
	UpstreamMessageTimeout         int    `json:"upstreamMessageTimeout"`    // ms - timeout for receiving messages from upstream WebSocket
	UpstreamReconnectInterval      int    `json:"upstreamReconnectInterval"` // ms - interval between reconnection attempts
	UpstreamPingInterval           int    `json:"upstreamPingInterval"`      // ms - interval for WebSocket ping to upstream; 0 = use default
	DedupCacheSize                 int    `json:"dedupCacheSize"`
	MaxSubscriptionsPerClient      int    `json:"maxSubscriptionsPerClient"`
	RetryEnabled                   bool   `json:"retryEnabled"`
	RetryMaxAttempts               int    `json:"retryMaxAttempts"`
	CircuitBreakerEnabled          bool   `json:"circuitBreakerEnabled"`
	CircuitBreakerFailureThreshold int    `json:"circuitBreakerFailureThreshold"`
	CircuitBreakerRecoveryTimeout  int    `json:"circuitBreakerRecoveryTimeout"`
	CircuitBreakerHalfOpenRequests int    `json:"circuitBreakerHalfOpenRequests"`
	// Sliding-window CB params: failures/total within the window trigger open.
	// Absolute FailureThreshold is preserved as an OR-trigger for backwards compatibility.
	CircuitBreakerWindowSize           int     `json:"circuitBreakerWindowSize"`           // ms; default 60000
	CircuitBreakerMinRequests          int     `json:"circuitBreakerMinRequests"`          // default 10
	CircuitBreakerFailureRateThreshold float64 `json:"circuitBreakerFailureRateThreshold"` // 0..1; default 0.5
	CircuitBreakerMaxEvents            int     `json:"circuitBreakerMaxEvents"`            // hard cap on events slice; default 10000
	// Per-RPC-call timeout for upstream WS requests (HTTP uses RequestTimeout via httpClient.Timeout).
	// Without this, hanging WS requests can sit until outer ctx (often 60-130s), and CB never sees them as failures fast enough.
	UpstreamRequestTimeout int             `json:"upstreamRequestTimeout"` // ms; default 15000
	WSSendTimeout          int             `json:"wsSendTimeout"`          // ms - timeout for sending to client WebSocket; 0 = use default
	Cache                  *CacheConfig    `json:"cache,omitempty"`
	Plugins                *PluginConfig   `json:"plugins,omitempty"`
	Batching               *BatchingConfig `json:"batching,omitempty"`
	Groups                 []GroupConfig   `json:"groups"`
}

// CacheConfig represents cache configuration
type CacheConfig struct {
	Enabled         bool     `json:"enabled"`
	TTL             int      `json:"ttl"`             // seconds
	Size            int      `json:"size"`            // number of entries
	DisabledMethods []string `json:"disabledMethods"` // methods to exclude from caching
}

// PluginConfig represents plugin configuration
type PluginConfig struct {
	Enabled   bool   `json:"enabled"`
	Directory string `json:"directory"` // path to plugins directory
	Timeout   int    `json:"timeout"`   // execution timeout in milliseconds
}

// BatchingConfig represents request batching configuration
type BatchingConfig struct {
	Enabled bool                         `json:"enabled"`
	Methods map[string]BatchMethodConfig `json:"methods"`
}

// BatchMethodConfig represents batching configuration for a specific method
type BatchMethodConfig struct {
	MaxSize        int  `json:"maxSize"`        // maximum batch size
	MaxWait        int  `json:"maxWait"`        // maximum wait time in milliseconds
	AggregateParam int  `json:"aggregateParam"` // index of the parameter to aggregate
	Spread         bool `json:"spread"`         // if true, params is the array itself (no nesting)
}

// GroupConfig represents a group of upstreams
type GroupConfig struct {
	Name         string                 `json:"name"`
	Upstreams    []UpstreamConfig       `json:"upstreams"`
	PluginParams map[string]interface{} `json:"pluginParams,omitempty"`
}

// UpstreamConfig represents a single upstream configuration
type UpstreamConfig struct {
	Name           string   `json:"name"`
	RPCURL         string   `json:"rpcUrl"`
	WSURL          string   `json:"wsUrl"`
	Weight         int      `json:"weight"`
	Role           Role     `json:"role"`
	PreferWS       bool     `json:"preferWs"`       // prefer WebSocket for RPC calls when both rpcUrl and wsUrl are configured (default: false)
	BlockedMethods []string `json:"blockedMethods"` // methods this upstream does not support
	// HistoricalBlockRange declares how many blocks behind currentBlock this upstream keeps state for
	// (typical for non-archive nodes: --gcmode=full keeps ~64-128 blocks).
	// 0 = unlimited (archive). When >0 and the requested block is older than currentBlock-HistoricalBlockRange,
	// the upstream is excluded via initialExclude before the WRR balancer.
	HistoricalBlockRange uint64 `json:"historicalBlockRange"`
}

// Default values
const (
	DefaultHost                               = "localhost"
	DefaultRPCPort                            = 8545
	DefaultWSPort                             = 8546
	DefaultLogLevel                           = "info"
	DefaultMaxBodySize                        = int64(0) // 0 means no limit
	DefaultRequestTimeout                     = 5000     // ms
	DefaultResponseHeaderTimeout              = 10000    // ms - max wait for upstream response headers
	DefaultIdleConnTimeout                    = 30000    // ms - idle keep-alive lifetime to upstreams
	DefaultHealthCheckInterval                = 10000    // ms
	DefaultStatusLogInterval                  = 5000     // ms
	DefaultStatsLogInterval                   = 60000    // ms - interval for logging request statistics
	DefaultBlockLagThreshold                  = uint64(0)
	DefaultLagRecoveryTimeout                 = 2000  // ms - time for lagging upstreams to catch up before marking unhealthy
	DefaultUpstreamMessageTimeout             = 60000 // ms - timeout for receiving messages from upstream WebSocket (60s)
	DefaultUpstreamReconnectInterval          = 5000  // ms - interval between reconnection attempts (5s)
	DefaultUpstreamPingInterval               = 20000 // ms - interval for WebSocket ping to upstream (20s)
	DefaultDedupCacheSize                     = 10000
	DefaultMaxSubscriptionsPerClient          = 100
	DefaultRetryEnabled                       = true
	DefaultRetryMaxAttempts                   = 3
	DefaultCircuitBreakerEnabled              = true
	DefaultCircuitBreakerFailureThreshold     = 5
	DefaultCircuitBreakerRecoveryTimeout      = 30000 // ms
	DefaultCircuitBreakerHalfOpenRequests     = 2
	DefaultCircuitBreakerWindowSize           = 60000 // ms - sliding window
	DefaultCircuitBreakerMinRequests          = 10
	DefaultCircuitBreakerFailureRateThreshold = 0.5
	DefaultCircuitBreakerMaxEvents            = 10000
	DefaultUpstreamRequestTimeout             = 15000 // ms - per-RPC-call timeout for WS
	DefaultUpstreamWeight                     = 1
	DefaultUpstreamRole                       = RoleMain
	DefaultPluginDirectory                    = "./plugins"
	DefaultPluginTimeout                      = 30000 // ms - default plugin execution timeout
	DefaultBatchMaxSize                       = 100   // default maximum batch size
	DefaultBatchMaxWait                       = 500   // ms - default maximum wait time for batching
	DefaultWSSendTimeout                      = 10000 // ms - default timeout for sending to client WebSocket (10s)
)

// GetRequestTimeoutDuration returns request timeout as time.Duration
func (c *Config) GetRequestTimeoutDuration() time.Duration {
	return time.Duration(c.RequestTimeout) * time.Millisecond
}

// GetResponseHeaderTimeoutDuration returns the HTTP response-header timeout as time.Duration.
// Defaults to DefaultResponseHeaderTimeout when not set.
func (c *Config) GetResponseHeaderTimeoutDuration() time.Duration {
	if c.ResponseHeaderTimeout <= 0 {
		return time.Duration(DefaultResponseHeaderTimeout) * time.Millisecond
	}
	return time.Duration(c.ResponseHeaderTimeout) * time.Millisecond
}

// GetIdleConnTimeoutDuration returns the idle keep-alive connection timeout as time.Duration.
// Defaults to DefaultIdleConnTimeout when not set.
func (c *Config) GetIdleConnTimeoutDuration() time.Duration {
	if c.IdleConnTimeout <= 0 {
		return time.Duration(DefaultIdleConnTimeout) * time.Millisecond
	}
	return time.Duration(c.IdleConnTimeout) * time.Millisecond
}

// GetHealthCheckIntervalDuration returns health check interval as time.Duration
func (c *Config) GetHealthCheckIntervalDuration() time.Duration {
	return time.Duration(c.HealthCheckInterval) * time.Millisecond
}

// GetStatusLogIntervalDuration returns status log interval as time.Duration
func (c *Config) GetStatusLogIntervalDuration() time.Duration {
	return time.Duration(c.StatusLogInterval) * time.Millisecond
}

// GetStatsLogIntervalDuration returns stats log interval as time.Duration
func (c *Config) GetStatsLogIntervalDuration() time.Duration {
	return time.Duration(c.StatsLogInterval) * time.Millisecond
}

// GetLagRecoveryTimeoutDuration returns lag recovery timeout as time.Duration
func (c *Config) GetLagRecoveryTimeoutDuration() time.Duration {
	return time.Duration(c.LagRecoveryTimeout) * time.Millisecond
}

// GetUpstreamMessageTimeoutDuration returns upstream message timeout as time.Duration
func (c *Config) GetUpstreamMessageTimeoutDuration() time.Duration {
	return time.Duration(c.UpstreamMessageTimeout) * time.Millisecond
}

// GetUpstreamReconnectIntervalDuration returns upstream reconnect interval as time.Duration
func (c *Config) GetUpstreamReconnectIntervalDuration() time.Duration {
	return time.Duration(c.UpstreamReconnectInterval) * time.Millisecond
}

// GetUpstreamRequestTimeoutDuration returns the per-RPC-call timeout for upstream WS requests as time.Duration.
// Defaults to DefaultUpstreamRequestTimeout when not set.
func (c *Config) GetUpstreamRequestTimeoutDuration() time.Duration {
	if c.UpstreamRequestTimeout <= 0 {
		return time.Duration(DefaultUpstreamRequestTimeout) * time.Millisecond
	}
	return time.Duration(c.UpstreamRequestTimeout) * time.Millisecond
}

// GetCircuitBreakerWindowSizeDuration returns sliding-window size as time.Duration.
func (c *Config) GetCircuitBreakerWindowSizeDuration() time.Duration {
	if c.CircuitBreakerWindowSize <= 0 {
		return time.Duration(DefaultCircuitBreakerWindowSize) * time.Millisecond
	}
	return time.Duration(c.CircuitBreakerWindowSize) * time.Millisecond
}

// GetUpstreamPingIntervalDuration returns upstream ping interval as time.Duration
func (c *Config) GetUpstreamPingIntervalDuration() time.Duration {
	if c.UpstreamPingInterval <= 0 {
		return time.Duration(DefaultUpstreamPingInterval) * time.Millisecond
	}
	return time.Duration(c.UpstreamPingInterval) * time.Millisecond
}

// GetCircuitBreakerRecoveryTimeoutDuration returns circuit breaker recovery timeout as time.Duration
func (c *Config) GetCircuitBreakerRecoveryTimeoutDuration() time.Duration {
	if c.CircuitBreakerRecoveryTimeout <= 0 {
		return time.Duration(DefaultCircuitBreakerRecoveryTimeout) * time.Millisecond
	}
	return time.Duration(c.CircuitBreakerRecoveryTimeout) * time.Millisecond
}

// GetWSSendTimeoutDuration returns WebSocket send timeout as time.Duration
func (c *Config) GetWSSendTimeoutDuration() time.Duration {
	if c.WSSendTimeout <= 0 {
		return time.Duration(DefaultWSSendTimeout) * time.Millisecond
	}
	return time.Duration(c.WSSendTimeout) * time.Millisecond
}

// IsCacheEnabled returns true if cache is configured and enabled
func (c *Config) IsCacheEnabled() bool {
	return c.Cache != nil && c.Cache.Enabled
}

// IsPluginsEnabled returns true if plugins are configured and enabled
func (c *Config) IsPluginsEnabled() bool {
	return c.Plugins != nil && c.Plugins.Enabled
}

// GetPluginDirectory returns the plugins directory path
func (c *Config) GetPluginDirectory() string {
	if c.Plugins == nil || c.Plugins.Directory == "" {
		return DefaultPluginDirectory
	}
	return c.Plugins.Directory
}

// GetPluginTimeoutDuration returns plugin timeout as time.Duration
func (c *Config) GetPluginTimeoutDuration() time.Duration {
	if c.Plugins == nil || c.Plugins.Timeout == 0 {
		return time.Duration(DefaultPluginTimeout) * time.Millisecond
	}
	return time.Duration(c.Plugins.Timeout) * time.Millisecond
}

// GetCacheTTLDuration returns cache TTL as time.Duration
func (c *CacheConfig) GetTTLDuration() time.Duration {
	return time.Duration(c.TTL) * time.Second
}

// IsBatchingEnabled returns true if batching is configured and enabled
func (c *Config) IsBatchingEnabled() bool {
	return c.Batching != nil && c.Batching.Enabled && len(c.Batching.Methods) > 0
}

// GetBatchingMethods returns the list of methods with batching enabled
func (c *Config) GetBatchingMethods() []string {
	if c.Batching == nil {
		return nil
	}
	methods := make([]string, 0, len(c.Batching.Methods))
	for method := range c.Batching.Methods {
		methods = append(methods, method)
	}
	return methods
}

// GetMaxWaitDuration returns max wait time as time.Duration
func (c *BatchMethodConfig) GetMaxWaitDuration() time.Duration {
	if c.MaxWait == 0 {
		return time.Duration(DefaultBatchMaxWait) * time.Millisecond
	}
	return time.Duration(c.MaxWait) * time.Millisecond
}
