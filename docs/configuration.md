# Configuration Reference

RPCGofer uses a JSON configuration file. All configuration options are documented below.

## Complete Configuration Structure

```json
{
  "host": "localhost",
  "rpcPort": 8545,
  "wsPort": 8546,
  "logLevel": "info",
  "maxBodySize": 0,
  "requestTimeout": 5000,
  "healthCheckInterval": 10000,
  "statusLogInterval": 5000,
  "statsLogInterval": 60000,
  "blockLagThreshold": 0,
  "blockTimeout": 30000,
  "dedupCacheSize": 10000,
  "maxSubscriptionsPerClient": 100,
  "retryEnabled": true,
  "retryMaxAttempts": 3,
  "cache": {
    "enabled": true,
    "ttl": 300,
    "size": 10000,
    "disabledMethods": []
  },
  "groups": [
    {
      "name": "ethereum",
      "upstreams": [
        {
          "name": "infura",
          "rpcUrl": "https://mainnet.infura.io/v3/API_KEY",
          "wsUrl": "wss://mainnet.infura.io/ws/v3/API_KEY",
          "weight": 10,
          "role": "main"
        }
      ]
    }
  ]
}
```

## Server Settings

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `host` | string | `"localhost"` | Host address to bind servers |
| `rpcPort` | int | `8545` | HTTP RPC server port |
| `wsPort` | int | `8546` | WebSocket server port |
| `logLevel` | string | `"info"` | Log level: `debug`, `info`, `warn`, `error` |
| `maxBodySize` | int64 | `0` | Maximum request body size in bytes (0 = unlimited) |
| `requestTimeout` | int | `5000` | Request timeout in milliseconds |

## Health Monitoring Settings

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `healthCheckInterval` | int | `10000` | Health check interval in milliseconds (for HTTP polling) |
| `statusLogInterval` | int | `5000` | Status logging interval in milliseconds |
| `statsLogInterval` | int | `60000` | Request statistics logging interval in milliseconds |
| `blockLagThreshold` | uint64 | `0` | Maximum allowed block lag before marking upstream unhealthy (0 = disabled) |
| `blockTimeout` | int | `30000` | Time in milliseconds without receiving a new block before marking upstream unhealthy |

## Retry Settings

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `retryEnabled` | bool | `true` | Enable automatic retries on failure |
| `retryMaxAttempts` | int | `3` | Maximum retry attempts |

## Subscription Settings

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `dedupCacheSize` | int | `10000` | Size of deduplication cache for subscription events |
| `maxSubscriptionsPerClient` | int | `100` | Maximum subscriptions per WebSocket client |

## Cache Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache.enabled` | bool | `false` | Enable response caching |
| `cache.ttl` | int | - | Cache TTL in seconds (required if enabled) |
| `cache.size` | int | - | Maximum cache entries (required if enabled) |
| `cache.disabledMethods` | []string | `[]` | Methods to exclude from caching |

## Groups Configuration

Groups define collections of upstream nodes for different blockchain networks.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `groups[].name` | string | Yes | Unique group name (used in URL path) |
| `groups[].upstreams` | array | Yes | List of upstream configurations |

## Upstream Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `upstreams[].name` | string | Required | Unique upstream name within group |
| `upstreams[].rpcUrl` | string | - | HTTP RPC endpoint URL |
| `upstreams[].wsUrl` | string | - | WebSocket endpoint URL |
| `upstreams[].weight` | int | `1` | Load balancing weight |
| `upstreams[].role` | string | `"main"` | Role: `"main"` or `"fallback"` |

At least one of `rpcUrl` or `wsUrl` is required per upstream.

## Configuration Examples

### Minimal Configuration

```json
{
  "groups": [
    {
      "name": "ethereum",
      "upstreams": [
        {
          "name": "primary",
          "rpcUrl": "https://eth.example.com"
        }
      ]
    }
  ]
}
```

### Production Configuration

```json
{
  "host": "0.0.0.0",
  "rpcPort": 8545,
  "wsPort": 8546,
  "logLevel": "info",
  "blockLagThreshold": 3,
  "blockTimeout": 30000,
  "statsLogInterval": 60000,
  "retryEnabled": true,
  "retryMaxAttempts": 3,
  "cache": {
    "enabled": true,
    "ttl": 300,
    "size": 50000,
    "disabledMethods": []
  },
  "groups": [
    {
      "name": "ethereum",
      "upstreams": [
        {
          "name": "infura",
          "rpcUrl": "https://mainnet.infura.io/v3/KEY",
          "wsUrl": "wss://mainnet.infura.io/ws/v3/KEY",
          "weight": 10,
          "role": "main"
        },
        {
          "name": "alchemy",
          "rpcUrl": "https://eth-mainnet.g.alchemy.com/v2/KEY",
          "wsUrl": "wss://eth-mainnet.g.alchemy.com/v2/KEY",
          "weight": 10,
          "role": "main"
        },
        {
          "name": "quicknode",
          "rpcUrl": "https://your-endpoint.quiknode.pro/KEY/",
          "weight": 5,
          "role": "main"
        },
        {
          "name": "public-llama",
          "rpcUrl": "https://eth.llamarpc.com",
          "weight": 1,
          "role": "fallback"
        }
      ]
    }
  ]
}
```

### Multi-Chain Configuration

```json
{
  "host": "0.0.0.0",
  "rpcPort": 8545,
  "wsPort": 8546,
  "groups": [
    {
      "name": "ethereum",
      "upstreams": [
        {
          "name": "eth-main",
          "rpcUrl": "https://eth-mainnet.example.com",
          "weight": 10,
          "role": "main"
        }
      ]
    },
    {
      "name": "polygon",
      "upstreams": [
        {
          "name": "polygon-main",
          "rpcUrl": "https://polygon-mainnet.example.com",
          "weight": 10,
          "role": "main"
        }
      ]
    },
    {
      "name": "arbitrum",
      "upstreams": [
        {
          "name": "arb-main",
          "rpcUrl": "https://arb-mainnet.example.com",
          "weight": 10,
          "role": "main"
        }
      ]
    }
  ]
}
```

## Validation Rules

1. At least one group must be defined
2. Group names must be unique
3. Each group must have at least one upstream
4. Upstream names must be unique within a group
5. Each upstream must have at least one of `rpcUrl` or `wsUrl`
6. Weight must be positive
7. Role must be `"main"` or `"fallback"`
8. Ports must be between 1 and 65535
9. Cache TTL and size must be positive when cache is enabled

## Environment Variables

While RPCGofer uses a JSON config file, you can use environment variable substitution in your deployment pipeline before starting the service. The config file path is specified via the `-config` flag:

```bash
./rpcgofer -config /path/to/config.json
```
