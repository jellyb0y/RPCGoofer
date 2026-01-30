# Request Batching

RPCGofer supports automatic request batching (coalescing) for methods that accept array parameters. Multiple concurrent requests are aggregated into a single batch, executed once, and results are distributed back to each caller.

## Why Use Batching

The primary goal of batching is to **reduce the number of expensive requests to rented RPC nodes**. Most providers (Infura, Alchemy, QuickNode, etc.) charge based on request count, not data volume.

Without batching, 100 clients simultaneously checking addresses for contracts generate 100 requests to upstream. With batching, this becomes 1 request with an array of 100 addresses - **100x cost reduction**.

### Zero Changes Required for Consumers

The key advantage is that **RPC consumers don't need to change anything**. Your existing services continue making the same single-address calls they always did:

```javascript
// Your service code stays exactly the same
const isContract = await rpc.call("custom_isContract", ["0xAAA"]);
```

RPCGofer transparently aggregates these calls behind the scenes. Consumers are completely unaware that their requests are being batched - they receive responses as if they made individual calls.

### Best Use Cases

Batching is especially effective for:
- **High-traffic services** with many parallel requests of the same type
- **Custom methods** (plugins) that interact with contracts via multicall
- **State-checking methods** for multiple addresses/tokens simultaneously
- **Cost optimization** when using paid RPC providers

## Overview

When multiple clients call the same method with array parameters simultaneously, batching combines them into one request:

```
Client 1: custom_isContract(["0xAAA"]) ---|
Client 2: custom_isContract(["0xBBB"]) ---|--> custom_isContract(["0xAAA","0xBBB","0xCCC"])
Client 3: custom_isContract(["0xCCC"]) ---|              |
                                                        v
Client 1 <-- [true]                              [true, false, true]
Client 2 <-- [false]
Client 3 <-- [true]
```

This significantly reduces the number of RPC calls to upstream nodes and improves throughput for batch-capable methods.

## Configuration

Add the `batching` section to your `config.json`:

```json
{
  "batching": {
    "enabled": true,
    "methods": {
      "custom_isContract": {
        "maxSize": 100,
        "maxWait": 500,
        "aggregateParam": 0
      }
    }
  }
}
```

### Configuration Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `enabled` | bool | `false` | Enable batching system |
| `methods` | object | - | Map of method names to their batching configuration |

### Method Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `maxSize` | int | `100` | Maximum number of elements in a batch |
| `maxWait` | int | `500` | Maximum wait time in milliseconds before flushing |
| `aggregateParam` | int | - | Index of the parameter that contains the array to aggregate |

## How It Works

### Batch Key

Requests are grouped by a **batch key** which consists of:
- Method name
- All parameters **except** the aggregate parameter

For example, with `aggregateParam: 0`:
```
custom_isContract(["0xA"], "latest")  --> key: "custom_isContract:["latest"]"
custom_isContract(["0xB"], "latest")  --> key: "custom_isContract:["latest"]" (same batch)
custom_isContract(["0xC"], "pending") --> key: "custom_isContract:["pending"]" (different batch)
```

### Flush Triggers

A batch is executed when either:
1. **maxSize reached** - Total aggregated elements reach the configured maximum
2. **maxWait expired** - Timer expires since the first request was added

### Result Distribution

After batch execution:
1. Response array is validated (must match total input count)
2. Results are sliced and distributed to each client based on how many elements they contributed

## Requirements for Batched Methods

Methods suitable for batching must:

1. **Accept an array parameter** - One of the parameters must be an array
2. **Return an array of the same size** - Result array length must equal input array length
3. **Maintain order** - Result order must match input order

Example compatible plugin:

```javascript
// @method custom_isContract
function execute(params, upstream) {
    // params = ["0xA", "0xB", "0xC"]
    var calls = params.map(function(addr) {
        return { method: "eth_getCode", params: [addr, "latest"] };
    });
    var results = upstream.batchCall(calls);
    // Must return array of same length
    return results.map(function(code) {
        return code !== "0x" && code.length > 2;
    });
}
```

## Important Notes

### Caching

Methods with batching enabled are **automatically excluded from caching**. This prevents inconsistencies between cached and batched results.

### Error Handling

**Result Size Mismatch:**
If the batch method returns a different number of results than expected, all clients in the batch receive an error:
```json
{
  "error": {
    "code": -32002,
    "message": "batch result size mismatch: expected 5, got 3"
  }
}
```

**Method Error:**
If the batch method returns a JSON-RPC error, all clients receive the same error.

### Multiple Elements per Request

A single client request can contribute multiple elements:

```
Client 1: custom_isContract(["0xA"])           --> contributes 1 element
Client 2: custom_isContract(["0xB", "0xC"])    --> contributes 2 elements
Client 3: custom_isContract(["0xD"])           --> contributes 1 element

Batch: custom_isContract(["0xA", "0xB", "0xC", "0xD"])
Result: [true, false, true, true]

Client 1 receives: [true]
Client 2 receives: [false, true]    (2 elements)
Client 3 receives: [true]
```

### Graceful Shutdown

On server shutdown, all pending batches are flushed before the server stops.

## Use Cases

### 1. Contract Address Detection

Check multiple addresses for contract code in one call:

```json
{
  "batching": {
    "enabled": true,
    "methods": {
      "custom_isContract": {
        "maxSize": 100,
        "maxWait": 200,
        "aggregateParam": 0
      }
    }
  }
}
```

### 2. Multi-call Aggregation

Aggregate multiple view function calls:

```json
{
  "batching": {
    "enabled": true,
    "methods": {
      "custom_multicall": {
        "maxSize": 50,
        "maxWait": 100,
        "aggregateParam": 0
      }
    }
  }
}
```

## Performance Tuning

### maxSize

- **Higher values** = fewer RPC calls, but larger requests
- **Lower values** = more responsive, but more RPC calls
- Consider upstream node limits and gas/data costs

### maxWait

- **Lower values** = more responsive, less aggregation
- **Higher values** = better aggregation, higher latency
- Balance between latency requirements and aggregation efficiency

### Recommended Starting Point

```json
{
  "maxSize": 100,
  "maxWait": 500
}
```

Adjust based on:
- Traffic patterns (high concurrency = shorter maxWait)
- Upstream node capabilities
- Latency requirements

## Limitations

1. **Array parameters only** - The aggregate parameter must be an array
2. **Same-length response** - Method must return array of same size as input
3. **Order preservation** - Results must maintain input order
4. **No nested batching** - Batched methods cannot trigger other batched methods
5. **HTTP only** - Batching works with HTTP RPC, not WebSocket subscriptions
