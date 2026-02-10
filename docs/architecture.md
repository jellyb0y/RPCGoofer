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
- **Retry**: Automatic retry with failover, block-aware upstream exclusion (see [Block-Aware Routing](./block-aware-routing.md))

### upstream/
Upstream node management:
- **Upstream**: Single node connection and execution (HTTP and optional WebSocket)
- **UpstreamWSClient**: Owns one WebSocket connection per upstream; multiplexes subscriptions and RPC
- **Pool**: Group of upstreams with shared health monitoring
- **Health Monitor**: Real-time health checking via WebSocket or polling

### balancer/
Load balancing algorithms:
- **Weighted Round-Robin**: Distributes requests based on configured weights
- **Simple Round-Robin**: Equal distribution

### blockparam/
Block parameter handling (shared by proxy and cache):
- **GetBlockParamIndex**: Index of block parameter per RPC method
- **GetRequestedBlockNumber**: Parse requested block from params (single block or range)
- **IsDynamicBlockParam**: Detect dynamic tags (latest, pending, etc.)

### cache/
Response caching system:
- **Memory Cache**: LRU cache with TTL support
- **Methods**: Cacheability rules for RPC methods (uses blockparam for block-dependent methods)
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
2. For upstreams with WebSocket: establish connection (one per upstream)
3. Health monitor spawned
4. For upstreams with WebSocket:
   - Subscribe to newHeads on the shared connection
   - Update block number on each header
5. For HTTP-only upstreams:
   - Poll eth_blockNumber periodically
6. Compare block numbers across upstreams
7. Mark upstream unhealthy if lag > threshold
```

The same WebSocket connection is used for newHeads subscription, other subscription types (logs, etc.), and RPC calls when `preferWs` is enabled.

## Batch Requests

RPCGofer natively supports JSON-RPC batch requests as defined in the JSON-RPC 2.0 specification.

### What is a Batch Request

A batch request is a JSON array containing multiple JSON-RPC request objects sent in a single HTTP request:

```json
[
  {"jsonrpc": "2.0", "method": "eth_blockNumber", "params": [], "id": 1},
  {"jsonrpc": "2.0", "method": "eth_chainId", "params": [], "id": 2},
  {"jsonrpc": "2.0", "method": "eth_getBalance", "params": ["0x...", "latest"], "id": 3}
]
```

The response is also a JSON array with corresponding results:

```json
[
  {"jsonrpc": "2.0", "result": "0x1234567", "id": 1},
  {"jsonrpc": "2.0", "result": "0x1", "id": 2},
  {"jsonrpc": "2.0", "result": "0x1bc16d674ec80000", "id": 3}
]
```

### How Batch Processing Works

```
1. Client sends batch request (JSON array)
2. Handler detects batch by checking first character '['
3. Parse all requests in the batch
4. For each request:
   a. Check if method is cacheable
   b. Lookup in cache
   c. Collect cache hits and misses
5. Send only uncached requests to upstream as batch
6. Upstream returns batch response
7. Cache successful responses individually
8. Merge cached and upstream responses
9. Return complete batch response to client
```

### Batch Request Features

| Feature | Description |
|---------|-------------|
| Automatic detection | Single vs batch determined by first character |
| Per-request caching | Each request in batch checked against cache individually |
| Atomic upstream call | Uncached requests sent as single batch to one upstream |
| Retry as unit | On failure, entire batch retried on next upstream |
| Response ordering | Responses returned in same order as requests |

### Batch Request Example

```bash
curl -X POST http://localhost:8545/ethereum \
  -H "Content-Type: application/json" \
  -d '[
    {"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1},
    {"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":2}
  ]'
```

### Batch over WebSocket

Batch requests are also supported over WebSocket connections:

```javascript
ws.send(JSON.stringify([
  {"jsonrpc": "2.0", "method": "eth_blockNumber", "params": [], "id": 1},
  {"jsonrpc": "2.0", "method": "eth_gasPrice", "params": [], "id": 2}
]));
```

Note: Subscription methods (`eth_subscribe`, `eth_unsubscribe`) in batch are processed individually, not as part of the batch response.

### Performance Considerations

- **Reduced latency**: Multiple requests in single HTTP roundtrip
- **Cache efficiency**: Cached responses returned immediately, only misses go to upstream
- **Network optimization**: Single connection used for entire batch
- **Error isolation**: Individual request errors don't fail entire batch

## Concurrency Model

- Each upstream pool runs independent health monitoring goroutines
- Each upstream with WebSocket has one connection; one reader goroutine dispatches RPC responses and subscription events
- SharedSubscription does not own connections; it subscribes via the upstream's shared connection
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
- Invalid params (-32602)
- Execution reverted
- Insufficient funds
- Nonce errors
