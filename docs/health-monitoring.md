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
1. Connect to upstream WebSocket
2. Subscribe to newHeads
3. For each new block header:
   - Extract block number
   - Update upstream's current block
   - Recalculate health status
4. On connection failure:
   - Mark upstream unhealthy
   - Attempt reconnection after 5 seconds
```

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

Setting `blockLagThreshold: 0` effectively disables lag-based health checks. Upstreams are only marked unhealthy if they fail to respond or exceed block timeout.

## Block Timeout

In addition to block lag, RPCGofer monitors how long since the last block was received from each upstream.

### Configuration

```json
{
  "blockTimeout": 30000
}
```

### How It Works

```
1. Each time a new block is received, timestamp is recorded
2. Periodic check runs (every blockTimeout/4, minimum 500ms)
3. For each upstream:
   - Calculate time since last block
   - If exceeds blockTimeout: mark unhealthy
4. When new block arrives: upstream can become healthy again
```

### Use Case

Block timeout is particularly important for detecting "stuck" upstreams that:
- Maintain WebSocket connection but stop sending blocks
- Have network issues causing delayed block propagation
- Are stuck on a fork or have consensus issues

### Examples

With `blockTimeout: 30000` (30 seconds):

| Upstream | Last Block | Time Since | Status |
|----------|------------|------------|--------|
| A | 5s ago | 5000ms | Healthy |
| B | 25s ago | 25000ms | Healthy |
| C | 35s ago | 35000ms | Unhealthy |

### Logging

```
WARN upstream block timeout, marking unhealthy upstream=nodeC timeSinceBlock=35s timeout=30s
```

## Health Status Transitions

### Healthy -> Unhealthy

Triggers:
- Block lag exceeds threshold
- Block timeout exceeded (no new blocks within configured time)
- WebSocket connection lost
- HTTP health check fails
- Request timeout

Logging:
```
WARN upstream is lagging, marking unhealthy upstream=nodeA currentBlock=999996 maxBlock=1000000 lag=4
```

### Unhealthy -> Healthy

Triggers:
- Block lag returns within threshold
- Connection re-established
- Health check succeeds

Logging:
```
INFO upstream recovered, marking healthy upstream=nodeA currentBlock=1000000 maxBlock=1000000
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

Periodic logging of request counts per upstream provides visibility into traffic distribution.

### Configuration

```json
{
  "statsLogInterval": 60000  // Log every 60 seconds
}
```

### Log Format

```
INFO request statistics group=ethereum totalRequests=150 interval=60s 
     infura=80 alchemy=65 public-fallback=5
```

### Statistics Fields

| Field | Description |
|-------|-------------|
| totalRequests | Total number of requests sent to all upstreams during the interval |
| interval | Duration of the statistics collection period |
| [upstream_name] | Number of requests sent to each specific upstream |

### How It Works

1. Each request to an upstream increments an atomic counter
2. Batch requests increment the counter by the number of requests in the batch
3. At each `statsLogInterval`, counters are read and reset to zero
4. Statistics reflect requests sent during the previous interval only

### Use Cases

- **Load balancing verification**: Ensure traffic is distributed according to weights
- **Upstream utilization monitoring**: Identify heavily/lightly used upstreams
- **Fallback usage tracking**: Monitor how often fallback upstreams are used
- **Capacity planning**: Understand request volumes per upstream

## Connection Management

### WebSocket Reconnection

```
1. Connection lost detected
2. Mark upstream unhealthy
3. Wait 5 seconds
4. Attempt reconnection
5. On success:
   - Re-subscribe to newHeads
   - Resume monitoring
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

Each upstream group has independent health monitoring.

```
Pool: ethereum
├── HealthMonitor
│   ├── Upstream: infura (WebSocket monitoring)
│   ├── Upstream: alchemy (WebSocket monitoring)
│   └── Upstream: public (HTTP polling)
│
Pool: polygon
├── HealthMonitor
│   ├── Upstream: polygon-main (WebSocket monitoring)
│   └── Upstream: polygon-backup (HTTP polling)
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
