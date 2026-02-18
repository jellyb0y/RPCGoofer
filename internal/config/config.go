package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	applyDefaults(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// applyDefaults sets default values for unset fields
func applyDefaults(cfg *Config) {
	if cfg.Host == "" {
		cfg.Host = DefaultHost
	}
	if cfg.RPCPort == 0 {
		cfg.RPCPort = DefaultRPCPort
	}
	if cfg.WSPort == 0 {
		cfg.WSPort = DefaultWSPort
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
	if cfg.MaxBodySize == 0 {
		cfg.MaxBodySize = DefaultMaxBodySize
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.HealthCheckInterval == 0 {
		cfg.HealthCheckInterval = DefaultHealthCheckInterval
	}
	if cfg.StatusLogInterval == 0 {
		cfg.StatusLogInterval = DefaultStatusLogInterval
	}
	if cfg.StatsLogInterval == 0 {
		cfg.StatsLogInterval = DefaultStatsLogInterval
	}
	// BlockLagThreshold default is 0, which is valid
	if cfg.LagRecoveryTimeout == 0 {
		cfg.LagRecoveryTimeout = DefaultLagRecoveryTimeout
	}
	if cfg.UpstreamMessageTimeout == 0 {
		cfg.UpstreamMessageTimeout = DefaultUpstreamMessageTimeout
	}
	if cfg.UpstreamReconnectInterval == 0 {
		cfg.UpstreamReconnectInterval = DefaultUpstreamReconnectInterval
	}
	if cfg.CircuitBreakerFailureThreshold == 0 {
		cfg.CircuitBreakerFailureThreshold = DefaultCircuitBreakerFailureThreshold
	}
	if cfg.CircuitBreakerRecoveryTimeout == 0 {
		cfg.CircuitBreakerRecoveryTimeout = DefaultCircuitBreakerRecoveryTimeout
	}
	if cfg.CircuitBreakerHalfOpenRequests == 0 {
		cfg.CircuitBreakerHalfOpenRequests = DefaultCircuitBreakerHalfOpenRequests
	}
	if cfg.DedupCacheSize == 0 {
		cfg.DedupCacheSize = DefaultDedupCacheSize
	}
	if cfg.MaxSubscriptionsPerClient == 0 {
		cfg.MaxSubscriptionsPerClient = DefaultMaxSubscriptionsPerClient
	}
	// RetryEnabled default handling - we need to check if it was explicitly set
	// Since bool default is false, we handle this differently in JSON parsing
	if cfg.RetryMaxAttempts == 0 {
		cfg.RetryMaxAttempts = DefaultRetryMaxAttempts
	}
	if cfg.WSSendTimeout == 0 {
		cfg.WSSendTimeout = DefaultWSSendTimeout
	}

	// Apply defaults to upstreams
	for i := range cfg.Groups {
		for j := range cfg.Groups[i].Upstreams {
			if cfg.Groups[i].Upstreams[j].Weight == 0 {
				cfg.Groups[i].Upstreams[j].Weight = DefaultUpstreamWeight
			}
			if cfg.Groups[i].Upstreams[j].Role == "" {
				cfg.Groups[i].Upstreams[j].Role = DefaultUpstreamRole
			}
		}
	}

	// Apply defaults to plugins
	if cfg.Plugins != nil && cfg.Plugins.Enabled {
		if cfg.Plugins.Directory == "" {
			cfg.Plugins.Directory = DefaultPluginDirectory
		}
		if cfg.Plugins.Timeout == 0 {
			cfg.Plugins.Timeout = DefaultPluginTimeout
		}
	}

	// Apply defaults to batching methods
	if cfg.Batching != nil && cfg.Batching.Enabled {
		for method, methodCfg := range cfg.Batching.Methods {
			if methodCfg.MaxSize == 0 {
				methodCfg.MaxSize = DefaultBatchMaxSize
			}
			if methodCfg.MaxWait == 0 {
				methodCfg.MaxWait = DefaultBatchMaxWait
			}
			cfg.Batching.Methods[method] = methodCfg
		}
	}
}

// validate checks the configuration for errors
func validate(cfg *Config) error {
	if len(cfg.Groups) == 0 {
		return errors.New("at least one group is required")
	}

	groupNames := make(map[string]bool)
	for i, group := range cfg.Groups {
		if group.Name == "" {
			return fmt.Errorf("group[%d]: name is required", i)
		}

		if groupNames[group.Name] {
			return fmt.Errorf("group[%d]: duplicate group name '%s'", i, group.Name)
		}
		groupNames[group.Name] = true

		if len(group.Upstreams) == 0 {
			return fmt.Errorf("group '%s': at least one upstream is required", group.Name)
		}

		upstreamNames := make(map[string]bool)
		for j, upstream := range group.Upstreams {
			if upstream.Name == "" {
				return fmt.Errorf("group '%s', upstream[%d]: name is required", group.Name, j)
			}

			if upstreamNames[upstream.Name] {
				return fmt.Errorf("group '%s': duplicate upstream name '%s'", group.Name, upstream.Name)
			}
			upstreamNames[upstream.Name] = true

			if upstream.RPCURL == "" && upstream.WSURL == "" {
				return fmt.Errorf("group '%s', upstream '%s': at least one of rpcUrl or wsUrl is required",
					group.Name, upstream.Name)
			}

			if upstream.Weight <= 0 {
				return fmt.Errorf("group '%s', upstream '%s': weight must be positive",
					group.Name, upstream.Name)
			}

			if upstream.Role != RoleMain && upstream.Role != RoleFallback {
				return fmt.Errorf("group '%s', upstream '%s': role must be 'main' or 'fallback'",
					group.Name, upstream.Name)
			}
		}
	}

	if cfg.RPCPort < 1 || cfg.RPCPort > 65535 {
		return fmt.Errorf("rpcPort must be between 1 and 65535")
	}

	if cfg.WSPort < 1 || cfg.WSPort > 65535 {
		return fmt.Errorf("wsPort must be between 1 and 65535")
	}

	validLogLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}
	if !validLogLevels[cfg.LogLevel] {
		return fmt.Errorf("logLevel must be one of: debug, info, warn, error")
	}

	if cfg.RequestTimeout < 0 {
		return fmt.Errorf("requestTimeout must be non-negative")
	}

	if cfg.HealthCheckInterval < 0 {
		return fmt.Errorf("healthCheckInterval must be non-negative")
	}

	if cfg.DedupCacheSize < 0 {
		return fmt.Errorf("dedupCacheSize must be non-negative")
	}

	if cfg.MaxSubscriptionsPerClient < 0 {
		return fmt.Errorf("maxSubscriptionsPerClient must be non-negative")
	}

	if cfg.RetryMaxAttempts < 0 {
		return fmt.Errorf("retryMaxAttempts must be non-negative")
	}

	// Validate cache config if provided
	if cfg.Cache != nil && cfg.Cache.Enabled {
		if cfg.Cache.TTL <= 0 {
			return fmt.Errorf("cache.ttl must be positive when cache is enabled")
		}
		if cfg.Cache.Size <= 0 {
			return fmt.Errorf("cache.size must be positive when cache is enabled")
		}
	}

	// Validate plugins config if provided
	if cfg.Plugins != nil && cfg.Plugins.Enabled {
		if cfg.Plugins.Timeout < 0 {
			return fmt.Errorf("plugins.timeout must be non-negative")
		}
	}

	// Validate batching config if provided
	if cfg.Batching != nil && cfg.Batching.Enabled {
		for method, methodCfg := range cfg.Batching.Methods {
			if methodCfg.MaxSize <= 0 {
				return fmt.Errorf("batching.methods[%s].maxSize must be positive", method)
			}
			if methodCfg.MaxWait <= 0 {
				return fmt.Errorf("batching.methods[%s].maxWait must be positive", method)
			}
			if methodCfg.AggregateParam < 0 {
				return fmt.Errorf("batching.methods[%s].aggregateParam must be non-negative", method)
			}
		}
	}

	return nil
}

// configWithRetryDefault is used for proper default handling of bool fields
type configWithRetryDefault struct {
	Config
	RetryEnabledPtr            *bool `json:"retryEnabled"`
	CircuitBreakerEnabledPtr   *bool `json:"circuitBreakerEnabled"`
}

// Load reads and parses the configuration file with proper bool default handling
func LoadWithDefaults(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// First unmarshal to check if retryEnabled was explicitly set
	var rawCfg configWithRetryDefault
	if err := json.Unmarshal(data, &rawCfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg := &rawCfg.Config

	// Handle retryEnabled default
	if rawCfg.RetryEnabledPtr != nil {
		cfg.RetryEnabled = *rawCfg.RetryEnabledPtr
	} else {
		cfg.RetryEnabled = DefaultRetryEnabled
	}
	// Handle circuitBreakerEnabled default
	if rawCfg.CircuitBreakerEnabledPtr != nil {
		cfg.CircuitBreakerEnabled = *rawCfg.CircuitBreakerEnabledPtr
	} else {
		cfg.CircuitBreakerEnabled = DefaultCircuitBreakerEnabled
	}

	applyDefaults(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}
