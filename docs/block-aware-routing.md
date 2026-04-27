# Block-Aware Routing

RPCGofer can route block-dependent RPC requests only to upstreams that have already synced the requested block. This avoids incorrect or empty responses when an upstream lags behind the chain.

## Problem

Many JSON-RPC methods depend on a block number (or block range). Examples:

- `eth_getBlockByNumber`, `eth_getBalance`, `eth_call`, `eth_getLogs`, `debug_traceBlockByNumber`, `trace_call`, and others.

If the client requests data for block `N` and the request is sent to an upstream whose current block is less than `N`, the upstream may return stale data, a different view of the chain, or an error. Round-robin alone does not consider the requested block, so such requests could hit a lagging node.

## Solution

Before selecting an upstream for a request, RPCGofer:

1. Determines whether the method is block-dependent and what block (or block range) is requested.
2. If a concrete block number is requested, builds a set of upstreams that have not yet reached that block (using each upstream's current block from health monitoring).
3. Additionally, excludes upstreams whose declared `historicalBlockRange` does not cover the requested block (the upstream has already pruned state for older blocks).
4. The remaining upstreams are passed to the round-robin selector.

An upstream is eligible only when **both**:
- `upstream.currentBlock >= requestedBlock` (caught up to the chain), AND
- `historicalBlockRange == 0` (archive) OR `requestedBlock + historicalBlockRange >= currentBlock` (request is within the retention window).

The same logic applies to batch requests, with two distinct bounds:
- the **maximum** requested block in the batch is checked against `currentBlock` (block-lag);
- the **minimum** requested block in the batch is checked against the `historicalBlockRange` window — because the upstream must serve every block in the batch; failing the oldest one fails the whole batch.

## Block-Param Module (blockparam)

The logic for "which methods have a block parameter and where it is" is implemented in a separate package, `internal/blockparam`, so it can be reused by the cache and the proxy.

### Supported Methods and Block Parameter Index

For methods with a single block parameter, the module knows the index of that parameter in the `params` array:

| Method | Block param index | Params shape (simplified) |
|--------|-------------------|---------------------------|
| eth_getBlockByNumber | 0 | [blockNumber] |
| eth_getCode, eth_getBalance, eth_getTransactionCount, eth_call | 1 | [address, blockNumber] or [callObject, blockNumber] |
| eth_getStorageAt | 2 | [address, position, blockNumber] |
| eth_getBlockTransactionCountByNumber | 0 | [blockNumber] |
| eth_getTransactionByBlockNumberAndIndex | 0 | [blockNumber, index] |
| eth_getBlockReceipts | 0 | [blockNumber] |
| eth_getProof | 2 | [address, keys, blockNumber] |
| debug_traceBlockByNumber | 0 | [blockNumber] |
| debug_traceCall | 1 | [callObject, blockNumber, tracerConfig] |
| trace_call | 1 | [callObject, blockNumber, traceTypes] |
| trace_callMany | 1 | [calls, blockNumber] |
| trace_replayBlockTransactions | 0 | [blockNumber, traceTypes] |

For methods with a block range, the block is taken from the first parameter (filter object):

| Method | Block range source |
|--------|--------------------|
| eth_getLogs | params[0].fromBlock, params[0].toBlock |
| trace_filter | params[0].fromBlock, params[0].toBlock |

For range methods, the "requested block" used for filtering upstreams is the maximum of `fromBlock` and `toBlock` (when both are concrete numbers). If either is a dynamic tag (e.g. `latest`), the request is not considered block-specific and no upstream exclusion by block is applied.

### Dynamic Block Tags

Block parameters can be either:

- A concrete block number in hex (e.g. `"0x1234567"`).
- A tag string: `latest`, `pending`, `earliest`, `safe`, `finalized`.

When the client uses a tag, the requested block is not a fixed number; the node is expected to use its current view. In that case RPCGofer does not exclude upstreams by block (all healthy upstreams remain in the pool for that request). The blockparam module treats these as "dynamic" and does not return a requested block number.

### API of the blockparam Package

- **GetBlockParamIndex(method string) int**  
  Returns the index of the block parameter for the given method, or `-1` if the method has no single-block parameter.

- **IsDynamicBlockParam(param json.RawMessage) bool**  
  Returns true if the given param is a dynamic block tag (latest, pending, etc.) or an object containing such a tag (e.g. `blockNumber: "latest"`).

- **GetRequestedBlockNumber(method string, params json.RawMessage) (uint64, bool)**  
  For single-block methods: parses the block parameter at the known index; returns the block number and `true` only when it is a concrete hex number. For `eth_getLogs` and `trace_filter`: parses the filter object and returns `max(fromBlock, toBlock)` and `true` only when both bounds are concrete numbers. In all other cases (no block param, dynamic tag, or parse error) returns `(0, false)`.

- **DynamicBlockTags**  
  Map of tag names (`latest`, `pending`, `earliest`, `safe`, `finalized`) used to detect dynamic block specifiers.

## How the Proxy Uses Block-Aware Exclusion

1. **Single request**  
   When handling a single JSON-RPC request, the proxy calls `blockparam.GetRequestedBlockNumber(req.Method, req.Params)`.  
   - If it gets a concrete block number `N > 0`, it builds an initial exclude set via `applyHistoricalAndLagExclude(pool, N, N, ...)`. For each upstream in `pool.GetForRequest()`:
     - if `upstream.GetCurrentBlock() < N` — exclude (lagging behind head);
     - if `r := upstream.GetHistoricalRange(); r > 0 && cur > r && N + r < cur` — exclude (state for block `N` was pruned).
   - This set is passed to the executor as `initialExclude`. The executor copies it into its internal "tried" map, so the balancer's `Next(exclude)` will never select those upstreams for this request.

2. **Batch request**  
   For a batch, the proxy computes `GetRequestedBlockNumber` for every request and tracks both `maxBlock` and `minBlock`. The exclusion uses both bounds:
   - if `upstream.GetCurrentBlock() < maxBlock` — exclude (lagging behind newest block requested);
   - if `r := upstream.GetHistoricalRange(); r > 0 && cur > r && minBlock + r < cur` — exclude (oldest block in batch is outside the upstream's retention window).
   The whole batch is then sent to an upstream chosen from the remaining set (one upstream handles the entire batch).

3. **No block or dynamic tag**  
   If `GetRequestedBlockNumber` returns `(0, false)` (no block param, or dynamic tag, or parse error), no block-based exclusion is built and the request is balanced as before (only health and retry-tried upstreams are excluded).

## Interaction with Health and Retry

- **Health**: The health monitor already tracks each upstream's current block and can mark an upstream unhealthy if it lags beyond `blockLagThreshold`. Block-aware routing adds an extra, per-request filter: even if an upstream is still "healthy" by lag threshold, it is excluded for this specific request if it has not yet reached the requested block.

- **Retry**: The initial exclude set is merged with the executor's "tried" set. If the first chosen upstream fails and the executor retries, it will again only pick from upstreams that are not in the combined set (including those excluded by block).

## Edge Cases

- **All upstreams excluded**: If every upstream has `currentBlock < requestedBlock` or fails the historical-range check, the initial exclude set contains all of them. The balancer will have no candidate and will return no upstream; the executor will fail with "no upstreams available" (same as when all are unhealthy or already tried). Configure at least one archive upstream (`historicalBlockRange: 0`, the default) per group to act as a backstop for old blocks.

- **Batch with mixed methods**: Some batch entries may have a block number, others may not (e.g. `eth_blockNumber`). The maximum requested block among those that have one is used for the lag check; the minimum is used for the historical-range check. Entries without a block number do not influence either bound.

- **eth_call with block object**: Some clients send the block as an object `{ blockNumber: "0x123" }`. The blockparam module parses both string and object forms and returns the numeric block when present.

- **uint64 underflow guard**: When `historicalBlockRange > currentBlock` (e.g. a node still syncing from genesis), the comparison `requestedBlock + range < currentBlock` is guarded by `currentBlock > range`, so a fresh node is not excluded by a misconfigured large range.

## Use in the Cache

The cache uses the same blockparam package to decide whether a request is cacheable. For methods that are cacheable only when the block is a specific number (e.g. `eth_getBlockByNumber`, `eth_call`), the cache calls `blockparam.GetBlockParamIndex` and `blockparam.IsDynamicBlockParam` to detect dynamic tags. If the block parameter is a tag like `latest`, the response is not cached. So the blockparam module is the single place that defines "block-dependent methods" and "dynamic block tags" for both routing and caching.

## Summary

| Aspect | Behavior |
|--------|----------|
| When block is concrete (hex number) | Upstreams are excluded if `currentBlock < requestedBlock` (lag) OR `historicalBlockRange > 0 && requestedBlock + range < currentBlock` (state pruned). |
| When block is a tag (latest, pending, etc.) | No block-based exclusion; normal health-aware round-robin. |
| Batch | `maxBlock` is used for the lag check, `minBlock` for the historical-range check. One upstream must satisfy both for the entire batch. |
| Module | `internal/blockparam` provides method index, dynamic-tag detection, and requested block number; used by proxy and cache. `historicalBlockRange` is per-upstream config; check is implemented in `internal/proxy/retry.go::applyHistoricalAndLagExclude`. |
