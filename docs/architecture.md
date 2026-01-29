# Architecture Overview

RPCGofer is built with a modular architecture that separates concerns into distinct components. This document describes the system architecture and request flow.

## System Components

```
                                    +------------------+
                                    |  Configuration   |
                                    |    (config/)     |
                                    +--------+---------+
                                             |
                                             v
+----------------+               +-------------------+               +------------------+
|    Clients     |  HTTP/WS     |      Server       |               |    Upstreams     |
|                +------------->|    (server/)      +-------------->|   (upstream/)    |
| - HTTP Client  |              |                   |               |                  |
| - WS Client    |              | - RPC Server      |               | - Infura         |
+----------------+              | - WS Server       |               | - Alchemy        |
                                +--------+----------+               | - Public nodes   |
                                         |                          +------------------+
                                         |
                    +--------------------+--------------------+
                    |                    |                    |
                    v                    v                    v
           +----------------+   +-----------------+   +------------------+
           |     Proxy      |   |      Cache      |   |   Subscription   |
           |   (proxy/)     |   |    (cache/)     |   |  (subscription/) |
           |                |   |                 |   |                  |
           | - Handler      |   | - Memory Cache  |   | - Manager        |
           | - Retry Logic  |   | - Noop Cache    |   | - Deduplicator   |
           | - Router       |   | - Cache Rules   |   | - Client Session |
           +----------------+   +-----------------+   +------------------+
                    |
                    v
           +----------------+
           |    Balancer    |
           |  (balancer/)   |
           |                |
           | - Weighted RR  |
           | - Round Robin  |
           +----------------+
```

## Module Structure

### config/
Configuration loading and validation. Supports JSON configuration files with default values for all optional fields.

### server/
Main server orchestration. Creates and manages HTTP RPC and WebSocket servers, initializes all components.

### proxy/
Request proxying logic including:
- **Handler**: HTTP request handling, batch support
- **Router**: Group-based request routing
- **Retry**: Automatic retry with failover

### upstream/
Upstream node management:
- **Upstream**: Single node connection and execution
- **Pool**: Group of upstreams with shared health monitoring
- **Health Monitor**: Real-time health checking via WebSocket or polling

### balancer/
Load balancing algorithms:
- **Weighted Round-Robin**: Distributes requests based on configured weights
- **Simple Round-Robin**: Equal distribution

### cache/
Response caching system:
- **Memory Cache**: LRU cache with TTL support
- **Methods**: Cacheability rules for RPC methods
- **Noop Cache**: Disabled cache implementation

### subscription/
WebSocket subscription management:
- **Manager**: Global subscription management
- **Client Session**: Per-client subscription state
- **Deduplicator**: Event deduplication across multiple upstreams

### jsonrpc/
JSON-RPC protocol implementation:
- Request/Response parsing and serialization
- Batch request support
- Error handling

### ws/
WebSocket handling:
- Connection upgrade
- Client message handling
- Subscription forwarding

## Request Flow

### HTTP RPC Request

```
1. Client sends HTTP POST to /{group_name}
2. Handler validates request format
3. Router resolves group to upstream pool
4. Cache lookup (if method is cacheable)
5. If cache miss:
   a. Balancer selects upstream
   b. Request executed on upstream
   c. On failure: retry on next upstream
   d. Response cached (if cacheable)
6. Response returned to client
```

### WebSocket Connection

```
1. Client connects to ws://host:port/{group_name}
2. Handler upgrades to WebSocket
3. Client object created with pool reference
4. For each message:
   a. Parse JSON-RPC request
   b. If eth_subscribe: create multi-upstream subscription
   c. If eth_unsubscribe: remove subscription
   d. Otherwise: forward to upstream (with cache)
5. Subscription events deduplicated and forwarded
```

### Health Monitoring Flow

```
1. Pool started for each group
2. Health monitor spawned for each upstream
3. For upstreams with WebSocket:
   - Subscribe to newHeads
   - Update block number on each header
4. For HTTP-only upstreams:
   - Poll eth_blockNumber periodically
5. Compare block numbers across upstreams
6. Mark upstream unhealthy if lag > threshold
```

## Concurrency Model

- Each upstream pool runs independent health monitoring goroutines
- WebSocket clients have separate read and write goroutines
- Subscriptions use goroutines per upstream connection
- LRU cache operations are thread-safe with mutex protection
- Balancer state protected by mutex

## Error Handling

### Retryable Errors
- Network errors (connection refused, timeout)
- Server errors (-32000 to -32099)
- Internal errors (-32603)

### Non-Retryable Errors
- Parse errors (-32700)
- Invalid request (-32600)
- Method not found (-32601)
- Invalid params (-32602)
- Execution reverted
- Insufficient funds
- Nonce errors
