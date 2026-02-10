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
	Host                      string        `json:"host"`
	RPCPort                   int           `json:"rpcPort"`
	WSPort                    int           `json:"wsPort"`
	LogLevel                  string        `json:"logLevel"`
	MaxBodySize               int64         `json:"maxBodySize"`
	RequestTimeout            int           `json:"requestTimeout"`
	HealthCheckInterval       int           `json:"healthCheckInterval"`
	StatusLogInterval         int           `json:"statusLogInterval"`
	StatsLogInterval          int           `json:"statsLogInterval"`
	BlockLagThreshold         uint64        `json:"blockLagThreshold"`
	LagRecoveryTimeout        int           `json:"lagRecoveryTimeout"`
	UpstreamMessageTimeout    int           `json:"upstreamMessageTimeout"`    // ms - timeout for receiving messages from upstream WebSocket
	UpstreamReconnectInterval int           `json:"upstreamReconnectInterval"` // ms - interval between reconnection attempts
	DedupCacheSize            int           `json:"dedupCacheSize"`
	MaxSubscriptionsPerClient int           `json:"maxSubscriptionsPerClient"`
	RetryEnabled              bool           `json:"retryEnabled"`
	RetryMaxAttempts          int            `json:"retryMaxAttempts"`
	WSSendTimeout             int            `json:"wsSendTimeout"`             // ms - timeout for sending to client WebSocket; 0 = use default
	Cache                     *CacheConfig    `json:"cache,omitempty"`
	Plugins                   *PluginConfig   `json:"plugins,omitempty"`
	Batching                  *BatchingConfig `json:"batching,omitempty"`
	Groups                    []GroupConfig   `json:"groups"`
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
	Enabled bool                          `json:"enabled"`
	Methods map[string]BatchMethodConfig  `json:"methods"`
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
	Name      string           `json:"name"`
	Upstreams []UpstreamConfig `json:"upstreams"`
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
}

// Default values
const (
	DefaultHost                      = "localhost"
	DefaultRPCPort                   = 8545
	DefaultWSPort                    = 8546
	DefaultLogLevel                  = "info"
	DefaultMaxBodySize               = int64(0) // 0 means no limit
	DefaultRequestTimeout            = 5000     // ms
	DefaultHealthCheckInterval       = 10000    // ms
	DefaultStatusLogInterval         = 5000     // ms
	DefaultStatsLogInterval          = 60000    // ms - interval for logging request statistics
	DefaultBlockLagThreshold         = uint64(0)
	DefaultLagRecoveryTimeout        = 2000  // ms - time for lagging upstreams to catch up before marking unhealthy
	DefaultUpstreamMessageTimeout    = 60000 // ms - timeout for receiving messages from upstream WebSocket (60s)
	DefaultUpstreamReconnectInterval = 5000  // ms - interval between reconnection attempts (5s)
	DefaultDedupCacheSize            = 10000
	DefaultMaxSubscriptionsPerClient = 100
	DefaultRetryEnabled              = true
	DefaultRetryMaxAttempts          = 3
	DefaultUpstreamWeight            = 1
	DefaultUpstreamRole              = RoleMain
	DefaultPluginDirectory           = "./plugins"
	DefaultPluginTimeout             = 30000 // ms - default plugin execution timeout
	DefaultBatchMaxSize              = 100   // default maximum batch size
	DefaultBatchMaxWait              = 500   // ms - default maximum wait time for batching
	DefaultWSSendTimeout             = 10000 // ms - default timeout for sending to client WebSocket (10s)
)

// GetRequestTimeoutDuration returns request timeout as time.Duration
func (c *Config) GetRequestTimeoutDuration() time.Duration {
	return time.Duration(c.RequestTimeout) * time.Millisecond
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
