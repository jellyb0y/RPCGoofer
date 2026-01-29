# Caching System

RPCGofer includes an intelligent caching system that stores responses for immutable blockchain data, reducing upstream load and improving response times.

## Cache Configuration

```json
{
  "cache": {
    "enabled": true,
    "ttl": 300,
    "size": 10000,
    "disabledMethods": ["eth_call", "eth_getLogs"]
  }
}
```

| Parameter | Description |
|-----------|-------------|
| `enabled` | Enable/disable caching |
| `ttl` | Time-to-live in seconds |
| `size` | Maximum number of cache entries |
| `disabledMethods` | List of methods to exclude from caching |

## Cacheable Methods

### Always Cacheable

These methods return immutable data that can always be cached:

| Method | Description |
|--------|-------------|
| `eth_getBlockByHash` | Block data by hash |
| `eth_getTransactionByHash` | Transaction data by hash |
| `eth_getTransactionReceipt` | Transaction receipt by hash |
| `eth_getBlockTransactionCountByHash` | Transaction count in block by hash |
| `eth_getTransactionByBlockHashAndIndex` | Transaction by block hash and index |
| `eth_chainId` | Chain ID (immutable) |
| `net_version` | Network version (immutable) |
| `debug_traceBlockByHash` | Block trace by hash |
| `debug_traceTransaction` | Transaction trace |
| `trace_transaction` | Transaction trace (OpenEthereum) |
| `trace_block` | Block trace |
| `trace_replayTransaction` | Transaction replay trace |

### Cacheable with Specific Block Number

These methods are cached only when a specific block number is provided (not `latest`, `pending`, etc.):

| Method | Block Parameter Position |
|--------|-------------------------|
| `eth_getBlockByNumber` | 1st parameter |
| `eth_getCode` | 2nd parameter |
| `eth_getBalance` | 2nd parameter |
| `eth_getStorageAt` | 3rd parameter |
| `eth_getTransactionCount` | 2nd parameter |
| `eth_call` | 2nd parameter |
| `eth_getBlockTransactionCountByNumber` | 1st parameter |
| `eth_getTransactionByBlockNumberAndIndex` | 1st parameter |
| `eth_getBlockReceipts` | 1st parameter |
| `eth_getProof` | 3rd parameter |
| `debug_traceBlockByNumber` | 1st parameter |
| `debug_traceCall` | 2nd parameter |
| `trace_call` | 2nd parameter |
| `trace_callMany` | 2nd parameter |
| `trace_replayBlockTransactions` | 1st parameter |

### Cacheable with Specific Block Range

These methods are cached only when both `fromBlock` and `toBlock` are specific numbers:

| Method | Description |
|--------|-------------|
| `eth_getLogs` | Event logs with filter |
| `trace_filter` | Trace filter |

## Dynamic Block Tags

The following block tags indicate dynamic data and prevent caching:

- `latest` - Most recent mined block
- `pending` - Pending state/transactions
- `earliest` - Genesis block (cacheable but treated as dynamic for simplicity)
- `safe` - Latest safe block
- `finalized` - Latest finalized block

### Examples

**Cacheable:**
```json
{"method": "eth_getBalance", "params": ["0x...", "0x1234567"]}
{"method": "eth_call", "params": [{...}, "0xabc123"]}
{"method": "eth_getLogs", "params": [{"fromBlock": "0x100", "toBlock": "0x200"}]}
```

**Not Cacheable:**
```json
{"method": "eth_getBalance", "params": ["0x...", "latest"]}
{"method": "eth_call", "params": [{...}, "pending"]}
{"method": "eth_getLogs", "params": [{"fromBlock": "0x100", "toBlock": "latest"}]}
{"method": "eth_blockNumber", "params": []}
```

## Cache Key Generation

Cache keys are generated from:
1. Group name
2. Method name
3. Normalized parameters hash

### Key Format

```
{group}:{method}:{paramsHash}
```

### Parameter Normalization

Parameters are normalized before hashing:
- JSON objects sorted by key
- Hex strings converted to lowercase
- Consistent JSON encoding

### Example

```
Request: eth_getBalance(0xABC..., 0x1234567) on "ethereum" group

Normalized params: ["0xabc...", "0x1234567"]
Hash: sha256(normalized) -> first 8 bytes -> hex

Cache key: ethereum:eth_getBalance:a1b2c3d4
```

## Cache Implementation

### Memory Cache

The default cache implementation is an LRU (Least Recently Used) cache with TTL support.

**Features:**
- Thread-safe operations
- Automatic eviction of least recently used entries when full
- TTL-based expiration
- Background cleanup of expired entries

**Structure:**
```go
type MemoryCache struct {
    cache *lru.Cache[string, *cacheEntry]
    ttl   time.Duration
}

type cacheEntry struct {
    data      []byte
    expiresAt time.Time
}
```

### Noop Cache

When caching is disabled, a no-op implementation is used that never stores or returns data.

## Cache Flow

### Read Request

```
1. Check if method is cacheable
2. If cacheable:
   a. Generate cache key
   b. Lookup in cache
   c. If found and not expired:
      - Return cached response
      - Update response ID to match request
   d. If not found: proceed to upstream
3. If not cacheable: proceed to upstream
```

### Write Response

```
1. Receive response from upstream
2. Check if:
   - Response is successful (no error)
   - Method is cacheable
3. If cacheable:
   a. Generate cache key
   b. Serialize response
   c. Store in cache with TTL
4. Return response to client
```

## Batch Request Caching

Batch requests are processed with per-request caching:

```
1. For each request in batch:
   a. Check cache
   b. Collect cache hits
   c. Collect cache misses
2. Send only cache misses to upstream
3. Merge responses
4. Cache successful responses from upstream
5. Return combined batch response
```

## Cache Statistics

Cache operations are logged at debug level:

```
DEBUG cache hit method=eth_getBalance cacheKey=ethereum:eth_getBalance:a1b2c3d4
DEBUG cached response method=eth_getBalance cacheKey=ethereum:eth_getBalance:a1b2c3d4
```

## Best Practices

### TTL Recommendations

| Use Case | Recommended TTL |
|----------|-----------------|
| High-traffic production | 300-600 seconds |
| Development/testing | 60-120 seconds |
| Archival queries | 3600+ seconds |

### Size Recommendations

| Traffic Level | Recommended Size |
|---------------|------------------|
| Low (<100 RPS) | 1,000-5,000 |
| Medium (100-1000 RPS) | 10,000-50,000 |
| High (>1000 RPS) | 50,000-100,000 |

### Considerations

1. **Memory usage**: Each entry stores the full response JSON
2. **Hit rate**: Monitor cache hit rate to optimize size
3. **TTL vs freshness**: Balance between performance and data freshness
4. **Multi-group**: Cache is shared across all groups (keys include group name)

## Disabling Cache

### Disable Caching Entirely

To disable caching entirely:

```json
{
  "cache": {
    "enabled": false
  }
}
```

Or omit the `cache` section from configuration.

### Disable Caching for Specific Methods

You can disable caching for specific methods while keeping caching enabled for others:

```json
{
  "cache": {
    "enabled": true,
    "ttl": 300,
    "size": 10000,
    "disabledMethods": ["eth_call", "eth_getLogs", "debug_traceCall"]
  }
}
```

This is useful when:
- Some methods return data that changes frequently even with the same parameters
- You need real-time data for specific operations
- Debugging issues where cached responses may be problematic
- Specific methods have high cardinality making caching inefficient

**Example use cases:**

| Use Case | Disabled Methods |
|----------|------------------|
| Real-time balance tracking | `eth_getBalance`, `eth_call` |
| Fresh log queries | `eth_getLogs` |
| Debug/trace accuracy | `debug_traceCall`, `trace_call` |
| Nonce-sensitive operations | `eth_getTransactionCount` |
