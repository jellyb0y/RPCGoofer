# Health Monitoring

RPCGofer continuously monitors the health of upstream nodes to ensure requests are routed only to healthy endpoints.

## Health Check Methods

### WebSocket Subscription (Preferred)

For upstreams with WebSocket URLs configured, health is monitored via `newHeads` subscription.

**Advantages:**
- Real-time block updates
- No polling overhead
- Immediate health status changes
- Low latency detection

**Flow:**
```
1. Pool starts: establish one WebSocket connection per upstream with wsUrl
2. Health monitor subscribes to newHeads on each connection
3. For each new block header:
   - Extract block number
   - Update upstream's current block
   - Recalculate health status
4. On connection failure:
   - Mark upstream unhealthy
   - UpstreamWSClient reconnects and re-subscribes automatically
```

The same connection is shared for subscriptions (newHeads, logs, etc.) and RPC calls when `preferWs` is enabled.

### HTTP Polling (Fallback)

For upstreams with only HTTP RPC URLs, health is monitored via periodic `eth_blockNumber` calls.

**Configuration:**
```json
{
  "healthCheckInterval": 10000  // Poll every 10 seconds
}
```

**Flow:**
```
1. Every healthCheckInterval:
   - Call eth_blockNumber on upstream
   - Update upstream's current block
   - Recalculate health status
2. On RPC failure:
   - Mark upstream unhealthy
   - Continue polling
```

## Block Lag Threshold

The primary health metric is block synchronization lag.

### Configuration

```json
{
  "blockLagThreshold": 3
}
```

### Calculation

```
maxBlock = highest block number across all upstreams in pool
upstreamBlock = current block number of specific upstream
lag = maxBlock - upstreamBlock

healthy = (lag <= blockLagThreshold)
```

### Examples

With `blockLagThreshold: 3`:

| Upstream | Block | Max Block | Lag | Status |
|----------|-------|-----------|-----|--------|
| A | 1000000 | 1000000 | 0 | Healthy |
| B | 999999 | 1000000 | 1 | Healthy |
| C | 999997 | 1000000 | 3 | Healthy |
| D | 999996 | 1000000 | 4 | Unhealthy |

### Threshold Value of 0

Setting `blockLagThreshold: 0` effectively disables lag-based health checks. Upstreams are only marked unhealthy if they fail to respond or lose connection.

## Lag Recovery Timeout

When an upstream falls behind the maximum block, it is given a grace period to catch up before being marked unhealthy.

### Configuration

```json
{
  "lagRecoveryTimeout": 2000
}
```

### How It Works

```
1. When any upstream receives a new block that becomes the new maxBlock:
   - A timer is scheduled for lagRecoveryTimeout duration
   - This creates a "catch-up window" for other upstreams
2. When the timer fires:
   - All upstreams are checked against that specific block
   - If lag > blockLagThreshold: mark unhealthy
3. When any upstream receives a block:
   - If its lag <= blockLagThreshold: mark healthy immediately
```

### Use Case

Lag recovery timeout ensures fair treatment of upstreams with slightly different block propagation times:
- Prevents false-positive unhealthy status for temporarily lagging upstreams
- Gives network propagation time to reach all providers
- Handles brief desynchronization gracefully

### Examples

With `blockLagThreshold: 1` and `lagRecoveryTimeout: 2000` (2 seconds):

```
Time 0ms:   Upstream A receives block 100 (new maxBlock)
            -> Timer scheduled for block 100
Time 500ms: Upstream B receives block 99 (lag=1, within threshold -> healthy)
Time 1000ms: Upstream C still at block 98
Time 2000ms: Timer fires for block 100
            -> Check upstream C: lag=2 > threshold=1 -> unhealthy
Time 2500ms: Upstream C receives block 100 (lag=0 -> healthy again)
```

### Logging

```
WARN upstream did not catch up in time, marking unhealthy upstream=nodeC currentBlock=98 targetBlock=100 lag=2
INFO upstream caught up, marking healthy upstream=nodeC currentBlock=100 maxBlock=100
```

## Health Status Transitions

### Healthy -> Unhealthy

Triggers:
- Block lag exceeds threshold after recovery timeout
- WebSocket connection lost
- HTTP health check fails
- Request timeout

Logging:
```
WARN upstream did not catch up in time, marking unhealthy upstream=nodeA currentBlock=999996 targetBlock=1000000 lag=4
```

### Unhealthy -> Healthy

Triggers:
- Block lag returns within threshold
- Connection re-established
- Health check succeeds

Logging:
```
INFO upstream caught up, marking healthy upstream=nodeA currentBlock=1000000 maxBlock=1000000
```

## Status Logging

Periodic status logs provide visibility into upstream health.

### Configuration

```json
{
  "statusLogInterval": 5000  // Log every 5 seconds
}
```

### Log Format

```
INFO upstreams status group=ethereum maxBlock=1000000 
     healthyMain=[nodeA(block=1000000) nodeB(block=999999)] 
     unhealthyMain=[nodeC(block=999990)] 
     healthyFallback=[backup1(block=1000000)] 
     unhealthyFallback=[]
```

### Status Categories

| Category | Description |
|----------|-------------|
| healthyMain | Main upstreams within lag threshold |
| unhealthyMain | Main upstreams exceeding lag threshold |
| healthyFallback | Fallback upstreams within lag threshold |
| unhealthyFallback | Fallback upstreams exceeding lag threshold |

## Request Statistics Logging

Periodic logging of request counts, subscription statistics, and popular methods provides visibility into traffic distribution and usage patterns.

### Configuration

```json
{
  "statsLogInterval": 60000  // Log every 60 seconds
}
```

### Log Format

```
INFO request statistics interval=60s
  total_requests: 1523
  total_sub_events: 48
  upstreams:
    alchemy:
      requests: 812
      subscriptions: 2
      sub_events: 24
    infura:
      requests: 711
      subscriptions: 2
      sub_events: 24
  top_methods:
     1. eth_call                                  456
     2. eth_getBalance                            234
     3. eth_blockNumber                           189
     4. eth_getTransactionReceipt                 156
     5. eth_getLogs                               123
     6. eth_getBlockByNumber                      98
     7. eth_subscribe                             67
     8. eth_chainId                               54
     9. eth_gasPrice                              43
    10. eth_getCode                               32
    11. eth_estimateGas                           28
    12. eth_getTransactionByHash                  21
    13. eth_sendRawTransaction                    12
    14. eth_getBlockByHash                        8
    15. eth_unsubscribe                           2
```

### Statistics Fields

| Field | Description |
|-------|-------------|
| total_requests | Total HTTP requests sent to all upstreams during the interval |
| total_sub_events | Total subscription events received from all upstreams |
| upstreams | Per-upstream breakdown of statistics |
| upstreams.[name].requests | HTTP requests sent to this upstream |
| upstreams.[name].subscriptions | Active WebSocket subscriptions on this upstream |
| upstreams.[name].sub_events | Subscription events received from this upstream |
| top_methods | Top 15 most called JSON-RPC methods during the interval |

### How It Works

1. Each request to an upstream increments an atomic counter
2. Batch requests increment the counter by the number of requests in the batch
3. Each subscription event from an upstream increments the sub_events counter
4. Active subscription count is tracked per upstream
5. Method calls are tracked globally and sorted by frequency
6. At each `statsLogInterval`, counters are read and reset to zero
7. Statistics reflect activity during the previous interval only

### Use Cases

- **Load balancing verification**: Ensure traffic is distributed according to weights
- **Upstream utilization monitoring**: Identify heavily/lightly used upstreams
- **Fallback usage tracking**: Monitor how often fallback upstreams are used
- **Capacity planning**: Understand request volumes per upstream
- **Subscription health**: Monitor subscription event flow from each upstream
- **API usage analysis**: Identify most popular methods for optimization
- **Client behavior patterns**: Understand what methods clients call most frequently

## Connection Management

### WebSocket Reconnection

The UpstreamWSClient owns the WebSocket connection and handles reconnection:

```
1. Connection lost detected (read error or message timeout)
2. Mark upstream unhealthy
3. Wait reconnectInterval (default: 5 seconds)
4. Attempt reconnection
5. On success:
   - Re-subscribe to all active subscriptions (newHeads, logs, etc.)
   - Resume event delivery and RPC
6. On failure:
   - Repeat from step 3
```

### HTTP Client Configuration

```go
Transport: &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 100,
    IdleConnTimeout:     90 * time.Second,
    DisableCompression:  true,
}
```

## Per-Group Monitoring

Each upstream group has independent health monitoring. Each upstream with wsUrl has one shared WebSocket connection used for newHeads, other subscriptions, and RPC (when preferWs).

```
Pool: ethereum
├── HealthMonitor
│   ├── Upstream: infura (1 WS conn: newHeads + RPC if preferWs)
│   ├── Upstream: alchemy (1 WS conn: newHeads + RPC if preferWs)
│   └── Upstream: public (HTTP polling only)
│
Pool: polygon
├── HealthMonitor
│   ├── Upstream: polygon-main (1 WS conn)
│   └── Upstream: polygon-backup (HTTP polling only)
```

## Best Practices

### Threshold Selection

| Network | Recommended Threshold |
|---------|----------------------|
| Ethereum Mainnet | 2-5 blocks |
| Polygon | 5-10 blocks |
| Arbitrum/Optimism | 10-20 blocks |
| Fast L2s | 50-100 blocks |

### Monitoring Recommendations

1. **Use WebSocket when available**: Provides more accurate and timely health data
2. **Set appropriate intervals**: Balance between accuracy and overhead
3. **Enable status logging**: Helps identify patterns and issues
4. **Monitor logs for transitions**: Health flapping may indicate upstream issues

### Common Issues

**Frequent health flapping:**
- Cause: Threshold too tight for network conditions
- Solution: Increase `blockLagThreshold`

**All upstreams unhealthy:**
- Cause: Max block calculation based on single fast node
- Solution: Use more homogeneous upstream set or increase threshold

**Delayed health updates:**
- Cause: Long polling interval
- Solution: Use WebSocket or reduce `healthCheckInterval`
