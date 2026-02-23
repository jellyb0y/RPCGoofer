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
   b. Adds client as subscriber to the subscription Registry (creates subscription if needed)
   c. Registry holds active subscriptions; upstreams register with Registry on connect/reconnect
   d. Returns client subscription ID
3. Events from any upstream:
   a. Upstream delivers event to Registry (DeliverEvent)
   b. Registry checks deduplication, then broadcasts to all subscribers (clients + health monitor)
```

### Benefits

- **Redundancy**: Events delivered even if one upstream fails
- **Lower latency**: First event from any upstream is delivered
- **Automatic failover**: Continues working if upstreams disconnect

## Automatic Reconnection

RPCGofer automatically reconnects to upstream WebSocket connections when they drop or timeout.

### How It Works

```
1. Upstream WebSocket connection drops or times out
2. Upstream notifies Registry (Unregister) so Registry clears all subscriptions for that upstream
3. RPCGofer waits for reconnectInterval (default: 5 seconds)
4. Attempts to reconnect; on success upstream registers again with Registry (Register)
5. Registry subscribes the upstream to all active subscriptions; events resume
6. If reconnect fails: waits and retries indefinitely
```

### Message Timeout Detection

If no message is received from an upstream within `upstreamMessageTimeout` (default: 60 seconds), the connection is considered stale and reconnection is triggered. This handles cases where:
- The upstream silently disconnects
- Network issues prevent messages from arriving
- The upstream stops sending events without closing the connection

### Configuration

| Parameter | Default | Description |
|-----------|---------|-------------|
| `upstreamMessageTimeout` | 60000 ms | Timeout for receiving messages from upstream |
| `upstreamReconnectInterval` | 5000 ms | Interval between reconnection attempts |

### Example Configuration

```json
{
  "upstreamMessageTimeout": 60000,
  "upstreamReconnectInterval": 5000
}
```

### Logging

Reconnection events are logged:

```
INF waiting before reconnection attempt upstream=infura interval=5s
WRN upstream read error, will attempt reconnection upstream=infura error="..."
INF successfully reconnected to upstream upstream=infura
```

## Subscription Registry (Connection Multiplexing)

RPCGofer uses a single **subscription Registry** per group to manage subscriptions. Each upstream with wsUrl has exactly one WebSocket connection; on connect or reconnect the upstream registers with the Registry and the Registry subscribes it to all active subscriptions.

### Architecture

```
Connection model (1 connection per upstream):

  Pool Start
       |
       v
  Upstream 1 (wsUrl) --> UpstreamWSClient --> 1 WebSocket connection
  Upstream 2 (wsUrl) --> UpstreamWSClient --> 1 WebSocket connection

  On connect/reconnect: upstream calls Registry.Register(name, target)
  Registry subscribes the upstream to all active (subType, params); events delivered via DeliverEvent

  Client 1 ──┐
  Client 2 ──┼──> Registry (active subscriptions) <── Register/Unregister ── Upstream 1, 2, ...
  Client 3 ──┘     newHeads, logs, etc.                  + DeliverEvent from upstreams

  Same connection used for:
  - newHeads subscription (health monitor + clients)
  - logs, newPendingTransactions, etc.
  - RPC calls when preferWs is true

  Total: 1 connection per upstream (not per subscription type or client)
```

### How It Works

1. **Pool** - at startup, establishes one WebSocket connection per upstream with wsUrl (UpstreamWSClient)
2. **Registry** - single source of truth per group: active subscriptions (by type+params), subscribers (clients + health monitor), and registered upstreams (each provides Subscribe/Unsubscribe via SubscriptionTarget)
3. On **connect/reconnect** the upstream calls `Registry.Register(name, target)`; the Registry immediately subscribes that upstream to all active subscriptions
4. On **disconnect** the upstream calls `Registry.Unregister(name)` so the Registry clears all subscription state for that upstream
5. **Subscribers** (clients, health monitor) subscribe through the Registry; events are delivered via `DeliverEvent` from upstreams

### Subscription Key

Subscriptions are grouped by type and parameters:

| Type | Key Example |
|------|-------------|
| `newHeads` | `newHeads:` (no params) |
| `logs` | `logs:a1b2c3d4` (hash of filter params) |
| `newPendingTransactions` | `newPendingTransactions:` |

### Benefits

- **Connection Efficiency**: One connection per upstream regardless of subscription types or client count
- **Resource Optimization**: Single deduplication cache per subscription type
- **Unified Event Flow**: Health monitoring and client subscriptions use the same event stream
- **RPC over WebSocket**: When `preferWs` is enabled, RPC calls use the same connection
- **Scalability**: Supports thousands of clients without proportional connection growth

### Internal Integration

The health monitoring system subscribes to `newHeads` through the same Registry:

```
Registry subscription "newHeads:"
├── HealthMonitor subscriber (updates block numbers, health status)
├── Client 1 subscriber
├── Client 2 subscriber
└── ...
```

This means:
- Health monitor receives the same deduplicated events as clients
- No duplicate WebSocket connections for internal monitoring
- Consistent view of blockchain state across all components
- After upstream reconnect, Registry re-subscribes the upstream so newHeads (and other subscriptions) resume without manual resubscribe

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
1. Upstream delivers event to Registry (DeliverEvent)
2. Registry generates deduplication key for the subscription
3. Check LRU cache:
   - If key exists: discard event (duplicate)
   - If key absent: add to cache, broadcast to all subscribers
```

Deduplication happens in the Registry per subscription key, before events are fanned out to subscribers. This ensures:
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
   a. Client added as subscriber in Registry (subscription created if not exists)
   b. If new subscription: Registry subscribes all registered upstreams to it
4. On disconnect:
   a. Client removed from Registry subscriptions
   b. Subscriptions with no subscribers are removed; Registry calls Unsubscribe on upstreams for that type
   c. Session removed
```

### Registry and Upstream Lifecycle

```
1. First subscriber requests subscription type
2. Registry creates subscription (key = type+params), adds subscriber
3. For each registered upstream: Registry calls target.Subscribe(subType, params), stores subID
4. Additional subscribers join the same subscription in the Registry
5. When last subscriber leaves:
   a. Registry calls Unsubscribe(subID) on each upstream for that subscription
   b. Subscription removed from Registry
6. When upstream connects/reconnects: it calls Registry.Register(name, target); Registry subscribes it to all active subscriptions
7. When upstream disconnects: it calls Registry.Unregister(name); Registry clears all state for that upstream
```

WebSocket connections are owned by the upstream and persist for the pool's lifetime. They are not closed when a subscription is removed from the Registry.

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

When an upstream disconnects:
1. Upstream calls Registry.Unregister(name) (on reconnect start or Close)
2. Registry removes all subscription state for that upstream
3. Remaining upstreams continue delivering events
4. When the upstream reconnects, it calls Registry.Register(name, target) and the Registry subscribes it again to all active subscriptions

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
