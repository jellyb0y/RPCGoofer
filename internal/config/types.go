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
	BlockTimeout              int           `json:"blockTimeout"`
	DedupCacheSize            int           `json:"dedupCacheSize"`
	MaxSubscriptionsPerClient int           `json:"maxSubscriptionsPerClient"`
	RetryEnabled              bool          `json:"retryEnabled"`
	RetryMaxAttempts          int           `json:"retryMaxAttempts"`
	Cache                     *CacheConfig  `json:"cache,omitempty"`
	Groups                    []GroupConfig `json:"groups"`
}

// CacheConfig represents cache configuration
type CacheConfig struct {
	Enabled         bool     `json:"enabled"`
	TTL             int      `json:"ttl"`             // seconds
	Size            int      `json:"size"`            // number of entries
	DisabledMethods []string `json:"disabledMethods"` // methods to exclude from caching
}

// GroupConfig represents a group of upstreams
type GroupConfig struct {
	Name      string           `json:"name"`
	Upstreams []UpstreamConfig `json:"upstreams"`
}

// UpstreamConfig represents a single upstream configuration
type UpstreamConfig struct {
	Name   string `json:"name"`
	RPCURL string `json:"rpcUrl"`
	WSURL  string `json:"wsUrl"`
	Weight int    `json:"weight"`
	Role   Role   `json:"role"`
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
	DefaultBlockTimeout              = 2000 // ms - time without new block before marking unhealthy
	DefaultDedupCacheSize            = 10000
	DefaultMaxSubscriptionsPerClient = 100
	DefaultRetryEnabled              = true
	DefaultRetryMaxAttempts          = 3
	DefaultUpstreamWeight            = 1
	DefaultUpstreamRole              = RoleMain
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

// GetBlockTimeoutDuration returns block timeout as time.Duration
func (c *Config) GetBlockTimeoutDuration() time.Duration {
	return time.Duration(c.BlockTimeout) * time.Millisecond
}

// IsCacheEnabled returns true if cache is configured and enabled
func (c *Config) IsCacheEnabled() bool {
	return c.Cache != nil && c.Cache.Enabled
}

// GetCacheTTLDuration returns cache TTL as time.Duration
func (c *CacheConfig) GetTTLDuration() time.Duration {
	return time.Duration(c.TTL) * time.Second
}
