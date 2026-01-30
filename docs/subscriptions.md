# WebSocket Subscriptions

RPCGofer provides full support for Ethereum WebSocket subscriptions (`eth_subscribe`/`eth_unsubscribe`) with multi-upstream aggregation and automatic event deduplication.

## Supported Subscription Types

| Type | Description |
|------|-------------|
| `newHeads` | New block headers |
| `logs` | Event logs matching filter |
| `newPendingTransactions` | New pending transaction hashes |
| `syncing` | Sync status changes |

## Connection Flow

```
1. Client connects to ws://host:port/{group_name}
2. Connection upgraded to WebSocket
3. Client session created
4. Client can:
   - Send JSON-RPC requests (forwarded to upstream)
   - Create subscriptions (eth_subscribe)
   - Cancel subscriptions (eth_unsubscribe)
5. Events forwarded to client until disconnect
```

## Creating Subscriptions

### eth_subscribe Request

```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscribe",
  "params": ["newHeads"],
  "id": 1
}
```

### Response

```json
{
  "jsonrpc": "2.0",
  "result": "0x1a2b3c4d5e6f7890",
  "id": 1
}
```

The `result` is the subscription ID used for receiving events and unsubscribing.

### Subscription with Parameters

For `logs` subscriptions:

```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscribe",
  "params": [
    "logs",
    {
      "address": "0xdAC17F958D2ee523a2206206994597C13D831ec7",
      "topics": ["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"]
    }
  ],
  "id": 1
}
```

## Receiving Events

Events are pushed to the client as JSON-RPC notifications:

### newHeads Event

```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscription",
  "params": {
    "subscription": "0x1a2b3c4d5e6f7890",
    "result": {
      "hash": "0x...",
      "parentHash": "0x...",
      "number": "0xf4240",
      "timestamp": "0x64a12345",
      "gasLimit": "0x1c9c380",
      "gasUsed": "0x1234567",
      "miner": "0x...",
      ...
    }
  }
}
```

### logs Event

```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscription",
  "params": {
    "subscription": "0x1a2b3c4d5e6f7890",
    "result": {
      "address": "0x...",
      "topics": ["0x...", "0x..."],
      "data": "0x...",
      "blockNumber": "0xf4240",
      "transactionHash": "0x...",
      "transactionIndex": "0x0",
      "blockHash": "0x...",
      "logIndex": "0x0",
      "removed": false
    }
  }
}
```

## Canceling Subscriptions

### eth_unsubscribe Request

```json
{
  "jsonrpc": "2.0",
  "method": "eth_unsubscribe",
  "params": ["0x1a2b3c4d5e6f7890"],
  "id": 2
}
```

### Response

```json
{
  "jsonrpc": "2.0",
  "result": true,
  "id": 2
}
```

## Multi-Upstream Aggregation

RPCGofer subscribes to multiple upstreams simultaneously for increased reliability.

### How It Works

```
1. Client subscribes to newHeads
2. RPCGofer:
   a. Generates client subscription ID
   b. Subscribes client to SharedSubscription (creates one if needed)
   c. SharedSubscription maintains connections to all upstreams
   d. Returns client subscription ID
3. Events from any upstream:
   a. Received by SharedSubscription
   b. Checked for duplicates (before fan-out)
   c. If unique: broadcast to all subscribers (clients + health monitor)
```

### Benefits

- **Redundancy**: Events delivered even if one upstream fails
- **Lower latency**: First event from any upstream is delivered
- **Automatic failover**: Continues working if upstreams disconnect

## Shared Subscriptions (Connection Multiplexing)

RPCGofer uses shared subscriptions to optimize WebSocket connections to upstreams. Instead of creating separate connections for each client, connections are shared among all clients with the same subscription type.

### Architecture

```
Without Shared Subscriptions (N clients x M upstreams connections):

  Client 1 ─────┬──> Upstream 1
               └──> Upstream 2
  Client 2 ─────┬──> Upstream 1
               └──> Upstream 2
  Client 3 ─────┬──> Upstream 1
               └──> Upstream 2

  Total: 6 WebSocket connections

With Shared Subscriptions (M connections, regardless of clients):

  Client 1 ──┐
  Client 2 ──┼──> SharedSubscription ───┬──> Upstream 1
  Client 3 ──┘       (newHeads)         └──> Upstream 2

  Total: 2 WebSocket connections
```

### How It Works

1. **SharedSubscriptionManager** - central manager for all shared subscriptions per group
2. **SharedSubscription** - one shared subscription per unique `(type, params)` combination
3. **Subscribers** - both clients and internal components (like health monitor) subscribe to shared subscriptions

### Subscription Key

Subscriptions are grouped by type and parameters:

| Type | Key Example |
|------|-------------|
| `newHeads` | `newHeads:` (no params) |
| `logs` | `logs:a1b2c3d4` (hash of filter params) |
| `newPendingTransactions` | `newPendingTransactions:` |

### Benefits

- **Connection Efficiency**: Only M connections to upstreams regardless of client count
- **Resource Optimization**: Single deduplication cache per subscription type
- **Unified Event Flow**: Health monitoring and client subscriptions use the same event stream
- **Scalability**: Supports thousands of clients without proportional connection growth

### Internal Integration

The health monitoring system uses shared subscriptions for `newHeads`:

```
SharedSubscription (newHeads)
├── HealthMonitor subscriber (updates block numbers, health status)
├── Client 1 subscriber
├── Client 2 subscriber
└── ...
```

This means:
- Health monitor receives the same deduplicated events as clients
- No duplicate WebSocket connections for internal monitoring
- Consistent view of blockchain state across all components

## Main/Fallback Synchronization for newHeads

When using both main and fallback upstreams, RPCGofer ensures that `newHeads` events are only sent to clients when the block is available on main upstreams.

### The Problem

Without synchronization:
1. Fallback upstream receives block #1000 first
2. Client receives newHeads notification for block #1000
3. Client makes RPC request (e.g., `debug_traceBlockByNumber`) for block #1000
4. RPC request goes to main upstream (which doesn't have block #1000 yet)
5. Request fails with "block not found"

### The Solution

For `newHeads` events from fallback upstreams:

```
1. Fallback receives new block
2. Check if any healthy main upstream has this block
3. If yes: forward event to client immediately
4. If no: wait for main to receive the block
5. If all main upstreams become unhealthy: forward event (fallback is only source)
```

### Flow Diagram

```
Fallback receives block #1000
         |
         v
   Main has block?
    /          \
  Yes           No
   |             |
   v             v
Forward      Main healthy?
to client    /          \
           Yes           No
            |             |
            v             v
         Wait for     Forward
         main block   to client
```

### Configuration

This behavior is automatic and requires no configuration. It uses the health monitoring system:
- `lagRecoveryTimeout`: Time window for lagging upstreams to catch up before being marked unhealthy
- `blockLagThreshold`: If main lags too far behind, it becomes unhealthy

When all main upstreams are unhealthy, fallback events are forwarded immediately.

### Logging

Events from main upstreams:
```
DEBUG forwarded event to client upstream=main-node subID=0x123 type=newHeads isMain=true
```

Events from fallback upstreams (after waiting for main):
```
DEBUG forwarded event to client upstream=fallback-node subID=0x123 type=newHeads isMain=false
```

## Event Deduplication

Since events are received from multiple upstreams, deduplication prevents duplicate events reaching subscribers.

### Configuration

```json
{
  "dedupCacheSize": 10000
}
```

### Deduplication Keys

| Subscription Type | Key Components |
|-------------------|----------------|
| `newHeads` | `block:{blockHash}` |
| `logs` | `log:{blockHash}:{txIndex}:{logIndex}` |
| `newPendingTransactions` | `tx:{txHash}` |

### Deduplication Flow

```
1. Event received from upstream by SharedSubscription
2. Generate deduplication key
3. Check LRU cache:
   - If key exists: discard event (duplicate)
   - If key absent: add to cache, broadcast to all subscribers
```

Deduplication happens at the SharedSubscription level, before events are fanned out to subscribers. This ensures:
- Each unique event is processed only once
- All subscribers receive the same deduplicated stream
- Efficient use of memory (single cache per subscription type)

## Subscription Limits

### Per-Client Limit

```json
{
  "maxSubscriptionsPerClient": 100
}
```

Prevents individual clients from creating too many subscriptions.

### Error on Limit

```json
{
  "jsonrpc": "2.0",
  "error": {
    "code": -32603,
    "message": "maximum subscriptions reached (100)"
  },
  "id": 1
}
```

## Session Management

### Client Session

Each WebSocket connection has an associated client session that tracks:
- Active subscriptions (references to shared subscriptions)
- Send function for events

### Session Lifecycle

```
1. WebSocket connection established
2. Session created on first subscription
3. For each subscription:
   a. Client added as subscriber to SharedSubscription
   b. SharedSubscription created if not exists
4. On disconnect:
   a. Client removed from all SharedSubscriptions
   b. Empty SharedSubscriptions cleaned up (connections closed)
   c. Session removed
```

### Shared Subscription Lifecycle

```
1. First subscriber requests subscription type
2. SharedSubscription created:
   a. Connects to all upstreams with WebSocket
   b. Subscribes to events on each upstream
   c. Starts event reading goroutines
3. Additional subscribers join existing SharedSubscription
4. When last subscriber leaves:
   a. Unsubscribe from all upstreams
   b. Close all WebSocket connections
   c. Remove SharedSubscription
```

## Error Handling

### No WebSocket Upstreams

```json
{
  "jsonrpc": "2.0",
  "error": {
    "code": -32603,
    "message": "no healthy upstreams with WebSocket available"
  },
  "id": 1
}
```

### Upstream Disconnection

When an upstream disconnects during subscription:
1. Log warning
2. Remove upstream from subscription
3. Continue with remaining upstreams
4. If all upstreams lost: subscription continues but receives no events

### Invalid Subscription Type

```json
{
  "jsonrpc": "2.0",
  "error": {
    "code": -32602,
    "message": "subscription type is required"
  },
  "id": 1
}
```

## Regular RPC over WebSocket

WebSocket connections also support regular JSON-RPC requests (not subscriptions):

```json
{"jsonrpc": "2.0", "method": "eth_blockNumber", "params": [], "id": 1}
```

These are:
- Checked against cache (if cacheable)
- Forwarded to upstream via HTTP (preferred) or WebSocket
- Responses cached if applicable

## Best Practices

### Client Implementation

1. **Handle reconnection**: Resubscribe after connection loss
2. **Track subscription IDs**: Store returned IDs for unsubscribing
3. **Process events asynchronously**: Don't block on event handling
4. **Implement backpressure**: Handle event bursts gracefully

### Server Configuration

1. **Set appropriate limits**: Balance between flexibility and protection
2. **Size dedup cache**: Large enough to cover event burst windows
3. **Use WebSocket upstreams**: Required for subscriptions
4. **Monitor subscription counts**: Watch for unusual patterns

## Example: Full Subscription Flow

```javascript
// Connect
const ws = new WebSocket('ws://localhost:8546/ethereum');

ws.onopen = () => {
  // Subscribe to new blocks
  ws.send(JSON.stringify({
    jsonrpc: '2.0',
    method: 'eth_subscribe',
    params: ['newHeads'],
    id: 1
  }));
};

ws.onmessage = (event) => {
  const data = JSON.parse(event.data);
  
  if (data.id === 1) {
    // Subscription response
    console.log('Subscribed:', data.result);
    subscriptionId = data.result;
  } else if (data.method === 'eth_subscription') {
    // Event notification
    console.log('New block:', data.params.result.number);
  }
};

// Later: unsubscribe
ws.send(JSON.stringify({
  jsonrpc: '2.0',
  method: 'eth_unsubscribe',
  params: [subscriptionId],
  id: 2
}));
```
