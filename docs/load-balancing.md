# Load Balancing

RPCGofer implements intelligent load balancing with support for weighted distribution and automatic failover.

## Weighted Round-Robin Algorithm

The primary load balancing algorithm is Weighted Round-Robin (WRR), which distributes requests based on configured weights.

### How It Works

1. Each upstream has a configurable weight (default: 1)
2. Upstreams with higher weights receive proportionally more requests
3. The algorithm cycles through upstreams, selecting each based on its weight
4. One WRR balancer instance per pool; its state is preserved between requests so that when multiple main upstreams are available, distribution stays even over time

### Example

With three upstreams configured as:
- Upstream A: weight 10
- Upstream B: weight 10
- Upstream C: weight 5

Out of every 25 requests:
- Upstream A receives ~10 requests (40%)
- Upstream B receives ~10 requests (40%)
- Upstream C receives ~5 requests (20%)

### Configuration

```json
{
  "upstreams": [
    {
      "name": "high-priority",
      "rpcUrl": "https://premium-node.example.com",
      "weight": 10
    },
    {
      "name": "low-priority",
      "rpcUrl": "https://budget-node.example.com",
      "weight": 2
    }
  ]
}
```

## Main vs Fallback Upstreams

RPCGofer distinguishes between main and fallback upstreams for prioritized traffic routing.

### Role Types

| Role | Description |
|------|-------------|
| `main` | Primary upstreams used under normal conditions |
| `fallback` | Backup upstreams used only when all main upstreams fail |

### Traffic Flow

```
1. Request arrives
2. Balancer checks healthy MAIN upstreams
3. If main upstreams available:
   - Select from main upstreams using weighted round-robin
   - Execute request
4. If all main upstreams failed/unavailable:
   - Switch to fallback upstreams
   - Log warning about fallback usage
   - Select from fallback upstreams using weighted round-robin
```

### Configuration Example

```json
{
  "upstreams": [
    {
      "name": "premium-1",
      "rpcUrl": "https://premium.infura.io/v3/KEY",
      "weight": 10,
      "role": "main"
    },
    {
      "name": "premium-2",
      "rpcUrl": "https://eth-mainnet.alchemyapi.io/v2/KEY",
      "weight": 10,
      "role": "main"
    },
    {
      "name": "public-backup",
      "rpcUrl": "https://eth.llamarpc.com",
      "weight": 5,
      "role": "fallback"
    }
  ]
}
```

## Retry Logic

When a request fails, RPCGofer can automatically retry on other available upstreams.

### Configuration

```json
{
  "retryEnabled": true,
  "retryMaxAttempts": 3
}
```

### Retry Behavior

1. Request sent to selected upstream
2. If request fails with retryable error:
   - Mark upstream as tried
   - Select next available upstream (excluding tried ones)
   - Retry request
3. Repeat until:
   - Request succeeds
   - Maximum attempts reached
   - All upstreams exhausted
   - Non-retryable error encountered

### Retryable vs Non-Retryable Errors

**Retryable Errors:**
- Network errors (connection refused, timeout)
- Server errors (HTTP 5xx)
- JSON-RPC internal errors (-32603)
- JSON-RPC server errors (-32000 to -32099)

**Non-Retryable Errors:**
- Parse errors (-32700)
- Invalid request (-32600)
- Invalid params (-32602)
- Execution reverted
- Insufficient funds
- Nonce errors

### Retry Example Flow

```
Attempt 1: Upstream A -> Network error
  -> Mark A as tried
  -> Retry on next upstream

Attempt 2: Upstream B -> Server error
  -> Mark B as tried
  -> All main upstreams tried
  -> Switch to fallback

Attempt 3: Upstream C (fallback) -> Success
  -> Return response
```

## Health-Aware Balancing

The balancer only considers healthy upstreams when selecting targets.

### Health States

| State | Description | Included in Balancing |
|-------|-------------|----------------------|
| Healthy | Block number within threshold | Yes |
| Unhealthy | Block lag exceeds threshold | No |
| Unreachable | Connection failed | No |

### Health Check Integration

```
1. Health monitor tracks each upstream's block number
2. Unhealthy upstreams automatically excluded from rotation
3. Recovered upstreams automatically re-included
4. Balancer state reset when upstream pool changes
```

## Block-Aware Exclusion

For block-dependent methods (e.g. `eth_getBlockByNumber`, `eth_call`, `eth_getLogs`), RPCGofer can exclude upstreams that have not yet synced the requested block. This avoids sending a request for block N to an upstream whose current block is below N. See [Block-Aware Routing](./block-aware-routing.md) for details.

## Upstream Exclusion

During retry cycles, previously tried upstreams are excluded from selection.

### Exclusion Map

```go
tried := map[string]bool{
    "upstream-a": true,  // Failed, exclude from selection
    "upstream-b": true,  // Failed, exclude from selection
}
// Only upstream-c and other untried upstreams can be selected
```

## Batch Request Handling

Batch requests are sent as a single unit to one upstream.

```
1. Batch request received
2. Single upstream selected
3. Entire batch sent to upstream
4. If any response has retryable error:
   - Entire batch retried on next upstream
5. Individual responses may be cached separately
```

## Performance Considerations

### Weight Recommendations

| Use Case | Recommended Weight |
|----------|-------------------|
| Premium/dedicated node | 10 |
| Shared node | 5 |
| Public/free endpoint | 1-2 |
| Rate-limited endpoint | 1 |

### Best Practices

1. **Use multiple main upstreams**: Provides redundancy and better distribution
2. **Always configure fallback**: Ensures availability during outages
3. **Match weights to capacity**: Higher weights for higher-capacity nodes
4. **Enable retry**: Handles transient failures gracefully
5. **Set appropriate retry attempts**: 3 is a good balance between resilience and latency
