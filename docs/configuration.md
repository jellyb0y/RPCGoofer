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
  "responseHeaderTimeout": 10000,
  "idleConnTimeout": 30000,
  "healthCheckInterval": 10000,
  "statusLogInterval": 5000,
  "statsLogInterval": 60000,
  "blockLagThreshold": 0,
  "lagRecoveryTimeout": 2000,
  "upstreamMessageTimeout": 60000,
  "upstreamReconnectInterval": 5000,
  "upstreamRequestTimeout": 15000,
  "dedupCacheSize": 10000,
  "maxSubscriptionsPerClient": 100,
  "retryEnabled": true,
  "retryMaxAttempts": 3,
  "circuitBreakerEnabled": true,
  "circuitBreakerFailureThreshold": 5,
  "circuitBreakerRecoveryTimeout": 30000,
  "circuitBreakerHalfOpenRequests": 2,
  "circuitBreakerWindowSize": 60000,
  "circuitBreakerMinRequests": 10,
  "circuitBreakerFailureRateThreshold": 0.5,
  "circuitBreakerMaxEvents": 10000,
  "cache": {
    "enabled": true,
    "ttl": 300,
    "size": 10000,
    "disabledMethods": []
  },
  "plugins": {
    "enabled": true,
    "directory": "./plugins",
    "timeout": 30000
  },
  "batching": {
    "enabled": true,
    "methods": {
      "custom_isContract": {
        "maxSize": 100,
        "maxWait": 500,
        "aggregateParam": 0
      }
    }
  },
  "groups": [
    {
      "name": "ethereum",
      "pluginParams": {
        "codeCheckerAddress": "0x1234..."
      },
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
| `requestTimeout` | int | `5000` | Overall HTTP request timeout in milliseconds (`httpClient.Timeout`, covers headers + body) |
| `responseHeaderTimeout` | int | `10000` | Max wait in ms for an upstream to start responding (response headers). Bounds the hang caused by a stale/dead pooled connection: instead of stalling for the full `requestTimeout`, the request fails fast and retry/last-resort can recover on a fresh connection. Set ≤ `requestTimeout` |
| `idleConnTimeout` | int | `30000` | How long an idle keep-alive connection to an upstream is kept before closing, in ms. Keep it **below** the upstream/load-balancer server-side idle timeout (~60s for Cloudflare/HAProxy); otherwise gofer reuses a connection the server already dropped and the next request hangs until `responseHeaderTimeout`. HTTP/2 connections are additionally kept healthy via keep-alive pings (ReadIdleTimeout 15s / PingTimeout 10s) |

## Health Monitoring Settings

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `healthCheckInterval` | int | `10000` | Health check interval in milliseconds (for HTTP polling) |
| `statusLogInterval` | int | `5000` | Status logging interval in milliseconds |
| `statsLogInterval` | int | `60000` | Request statistics logging interval in milliseconds |
| `blockLagThreshold` | uint64 | `0` | Maximum allowed block lag before marking upstream unhealthy (0 = disabled) |
| `lagRecoveryTimeout` | int | `2000` | Time in milliseconds for lagging upstreams to catch up before marking unhealthy |

## Retry Settings

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `retryEnabled` | bool | `true` | Enable automatic retries on failure |
| `retryMaxAttempts` | int | `3` | Maximum retry attempts |

## Circuit Breaker Settings

The circuit breaker temporarily excludes an upstream from selection when transport-level failures (network errors, HTTP 5xx, timeouts) accumulate. JSON-RPC errors returned in the response body (HTTP 200 + `error` field) are NOT counted as failures — those are logical errors and the upstream itself is healthy.

It opens when **either** trigger fires within the sliding window:
- failures ≥ `circuitBreakerFailureThreshold` (absolute trigger);
- total events ≥ `circuitBreakerMinRequests` AND failures/total ≥ `circuitBreakerFailureRateThreshold` (rate trigger).

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `circuitBreakerEnabled` | bool | `true` | Enable circuit breaker. When disabled, all requests are allowed unconditionally. |
| `circuitBreakerFailureThreshold` | int | `5` | Absolute number of failures within the window that opens the circuit |
| `circuitBreakerRecoveryTimeout` | int | `30000` | After opening, how long (ms) before a probe request is allowed (transition to half-open) |
| `circuitBreakerHalfOpenRequests` | int | `2` | Number of consecutive successes in half-open required to fully close the circuit |
| `circuitBreakerWindowSize` | int | `60000` | Sliding window size in milliseconds. Older events are pruned and no longer counted |
| `circuitBreakerMinRequests` | int | `10` | Minimum events in the window before the rate trigger applies |
| `circuitBreakerFailureRateThreshold` | float | `0.5` | Failure rate (0..1) that triggers opening, when total ≥ minRequests |
| `circuitBreakerMaxEvents` | int | `10000` | Hard cap on the events buffer per upstream. Under very high RPS, the effective window may shrink to keep memory bounded |

## Subscription Settings

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `dedupCacheSize` | int | `10000` | Size of deduplication cache for subscription events |
| `maxSubscriptionsPerClient` | int | `100` | Maximum subscriptions per WebSocket client |

## Upstream WebSocket Settings

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `upstreamMessageTimeout` | int | `60000` | Timeout in milliseconds for receiving any message on the upstream WebSocket connection (read deadline). If no message is received within this period, the connection is considered broken and reconnection is attempted |
| `upstreamReconnectInterval` | int | `5000` | Interval in milliseconds between reconnection attempts to upstream WebSocket |
| `upstreamRequestTimeout` | int | `15000` | Per-RPC-call timeout in milliseconds for individual WebSocket requests (`SendRequest`). Without it, a hanging request would only fail when the outer client context expires (often 60-130s), preventing the circuit breaker from reacting in time. HTTP requests use `requestTimeout` (httpClient timeout) instead |

These settings control automatic reconnection behavior for upstream WebSocket connections used by subscriptions. When an upstream WebSocket connection drops or times out, RPCGofer will automatically attempt to reconnect.

## Cache Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cache.enabled` | bool | `false` | Enable response caching |
| `cache.ttl` | int | - | Cache TTL in seconds (required if enabled) |
| `cache.size` | int | - | Maximum cache entries (required if enabled) |
| `cache.disabledMethods` | []string | `[]` | Methods to exclude from caching |

## Plugins Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `plugins.enabled` | bool | `false` | Enable JavaScript plugins |
| `plugins.directory` | string | `"./plugins"` | Path to plugins directory |
| `plugins.timeout` | int | `30000` | Plugin execution timeout in milliseconds |

The `directory` path is relative to the working directory where RPCGofer is started:
- **Binary**: Relative to current working directory or use absolute path
- **Docker**: Working directory is `/app`, use `"/app/plugins"` and mount the volume

See [Plugins Documentation](./plugins.md) for details on writing and configuring plugins.

## Batching Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `batching.enabled` | bool | `false` | Enable request batching |
| `batching.methods` | object | - | Map of method names to batching configuration |
| `batching.methods[].maxSize` | int | `100` | Maximum number of elements in a batch |
| `batching.methods[].maxWait` | int | `500` | Maximum wait time in milliseconds |
| `batching.methods[].aggregateParam` | int | - | Index of the parameter to aggregate |

Batched methods are automatically excluded from caching. The method must accept an array parameter and return an array of the same size.

See [Batching Documentation](./batching.md) for details on request coalescing.

## Groups Configuration

Groups define collections of upstream nodes for different blockchain networks.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `groups[].name` | string | Yes | Unique group name (used in URL path) |
| `groups[].pluginParams` | object | No | Key-value parameters injected into plugin JS runtime as the `config` object. Allows different chains to use different contract addresses or settings |
| `groups[].upstreams` | array | Yes | List of upstream configurations |

## Upstream Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `upstreams[].name` | string | Required | Unique upstream name within group |
| `upstreams[].rpcUrl` | string | - | HTTP RPC endpoint URL |
| `upstreams[].wsUrl` | string | - | WebSocket endpoint URL |
| `upstreams[].weight` | int | `1` | Load balancing weight |
| `upstreams[].role` | string | `"main"` | Role: `"main"` or `"fallback"` |
| `upstreams[].preferWs` | bool | `false` | When both `rpcUrl` and `wsUrl` are set, prefer WebSocket for RPC calls. Batch requests always use HTTP when available |
| `upstreams[].blockedMethods` | array | `[]` | Methods this upstream does not support. These upstreams will not be selected for corresponding requests |
| `upstreams[].historicalBlockRange` | uint64 | `0` | Retention window in blocks. `0` = unlimited (archive node). When `>0` the upstream is considered able to serve only blocks in `[currentBlock - historicalBlockRange, currentBlock]` and is excluded from selection for requests on older blocks (e.g. a Geth full-node with `--gcmode=full` keeps roughly 64-128 blocks of state — set `historicalBlockRange: 128` to prevent trace requests on older blocks from being routed to it). See [Block-Aware Routing](./block-aware-routing.md) |

At least one of `rpcUrl` or `wsUrl` is required per upstream. By default, HTTP is preferred over WebSocket when both are configured.

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
  "lagRecoveryTimeout": 2000,
  "upstreamMessageTimeout": 60000,
  "upstreamReconnectInterval": 5000,
  "statsLogInterval": 60000,
  "retryEnabled": true,
  "retryMaxAttempts": 3,
  "cache": {
    "enabled": true,
    "ttl": 300,
    "size": 50000,
    "disabledMethods": []
  },
  "plugins": {
    "enabled": true,
    "directory": "/app/plugins",
    "timeout": 30000
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
      "pluginParams": {
        "codeCheckerAddress": "0x1111111111111111111111111111111111111111"
      },
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
      "pluginParams": {
        "codeCheckerAddress": "0x2222222222222222222222222222222222222222"
      },
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
10. `circuitBreakerFailureRateThreshold` must be in `[0, 1]`
11. Other numeric circuit breaker / timeout fields must be non-negative

## Environment Variables

While RPCGofer uses a JSON config file, you can use environment variable substitution in your deployment pipeline before starting the service. The config file path is specified via the `-config` flag:

```bash
./rpcgofer -config /path/to/config.json
```
