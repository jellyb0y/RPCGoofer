# Plugins

RPCGofer supports JavaScript plugins for creating custom RPC methods. Plugins allow you to implement complex blockchain operations that combine multiple RPC calls, data transformations, and custom logic without modifying the core proxy.

## Overview

Plugins are JavaScript files that define custom RPC methods. When a client calls a plugin method, RPCGofer:

1. Intercepts the request before sending to upstream
2. Executes the JavaScript plugin
3. The plugin can make upstream RPC calls via the `upstream` object
4. Returns the plugin's result to the client

## Configuration

### Basic Configuration

Add the `plugins` section to your `config.json`:

```json
{
  "plugins": {
    "enabled": true,
    "directory": "./plugins",
    "timeout": 30000
  }
}
```

### Configuration Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `enabled` | bool | `false` | Enable plugin system |
| `directory` | string | `"./plugins"` | Path to plugins directory |
| `timeout` | int | `30000` | Plugin execution timeout in milliseconds |

### Directory Path Configuration

**ATTENTION!** The `directory` path is relative to the working directory where RPCGofer is started. This is important for different deployment scenarios:

#### Running as Binary

When running RPCGofer directly as a binary:

```bash
# If you run from project root
cd /opt/rpcgofer
./rpcgofer -config config.json
# plugins directory = /opt/rpcgofer/plugins

# Or with absolute path
./rpcgofer -config config.json
# config: "directory": "/opt/rpcgofer/plugins"
```

#### Running in Docker

In Docker, the working directory is `/app` (as defined in Dockerfile). Configure the path accordingly:

```json
{
  "plugins": {
    "enabled": true,
    "directory": "/app/plugins",
    "timeout": 30000
  }
}
```

Mount your plugins directory when running the container:

```bash
docker run -v ./config.json:/app/config.json \
           -v ./plugins:/app/plugins \
           -p 8545:8545 -p 8546:8546 \
           rpcgofer
```

Or in `docker-compose.yml`:

```yaml
services:
  rpcgofer:
    image: rpcgofer
    volumes:
      - ./config.json:/app/config.json
      - ./plugins:/app/plugins
    ports:
      - "8545:8545"
      - "8546:8546"
```

#### Path Examples by Environment

| Environment | Working Directory | Recommended `directory` |
|-------------|-------------------|-------------------------|
| Binary (local) | Project root | `"./plugins"` |
| Binary (systemd) | Varies | Absolute path: `"/opt/rpcgofer/plugins"` |
| Docker | `/app` | `"/app/plugins"` |
| Kubernetes | `/app` | `"/app/plugins"` (mount ConfigMap or Volume) |

## Writing Plugins

### Plugin Structure

Each plugin is a `.js` file with two required elements:

1. **`@method` directive** - specifies the RPC method name
2. **`execute` function** - handles the request

```javascript
// @method custom_methodName
function execute(params, upstream) {
    // Your logic here
    return result;
}
```

### Minimal Example

```javascript
// @method custom_blockNumber
function execute(params, upstream) {
    var result = upstream.call("eth_blockNumber", []);
    // Convert hex to decimal
    return parseInt(result, 16);
}
```

### Complete Example: isContract

```javascript
// @method custom_isContract
// Checks if addresses are contracts

function execute(params, upstream) {
    // params = ["0x1234...", "0x5678..."]
    if (!params || !Array.isArray(params)) {
        throw new Error("params must be array of addresses");
    }

    if (params.length === 0) {
        return [];
    }

    // Validate addresses
    for (var i = 0; i < params.length; i++) {
        var addr = params[i];
        if (typeof addr !== "string" || !addr.match(/^0x[a-fA-F0-9]{40}$/)) {
            throw new Error("invalid address at index " + i);
        }
    }

    // Build batch request
    var calls = params.map(function(addr) {
        return { 
            method: "eth_getCode", 
            params: [addr, "latest"] 
        };
    });

    // Execute batch
    var results = upstream.batchCall(calls);

    // Convert to boolean array
    return results.map(function(code) {
        return typeof code === "string" && code.length > 2;
    });
}
```

Usage:

```bash
curl -X POST http://localhost:8545/ethereum \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "custom_isContract",
    "params": ["0xdAC17F958D2ee523a2206206994597C13D831ec7", "0x0000000000000000000000000000000000000001"],
    "id": 1
  }'

# Response: {"jsonrpc":"2.0","result":[true, false],"id":1}
```

## Available APIs

### upstream Object

The `upstream` object provides methods to call upstream RPC endpoints.

#### upstream.call(method, params)

Executes a single RPC call.

```javascript
var blockNumber = upstream.call("eth_blockNumber", []);
var balance = upstream.call("eth_getBalance", ["0x1234...", "latest"]);
var block = upstream.call("eth_getBlockByNumber", ["latest", false]);
```

**Parameters:**
- `method` (string) - RPC method name
- `params` (array) - Method parameters

**Returns:** The result field from the RPC response (parsed JSON).

**Throws:** Error if the RPC call fails.

#### upstream.batchCall(calls)

Executes multiple RPC calls in a single batch request.

```javascript
var results = upstream.batchCall([
    { method: "eth_blockNumber", params: [] },
    { method: "eth_gasPrice", params: [] },
    { method: "eth_getBalance", params: ["0x1234...", "latest"] }
]);
// results = [blockNumber, gasPrice, balance]
```

**Parameters:**
- `calls` (array) - Array of objects with `method` and `params` fields

**Returns:** Array of results in the same order as input calls.

**Throws:** Error if the batch call fails.

### utils Object

Utility functions for blockchain data manipulation.

#### utils.hexToBytes(hex)

Converts hex string to byte array.

```javascript
var bytes = utils.hexToBytes("0x1234");
// bytes = [0x12, 0x34]
```

#### utils.bytesToHex(bytes)

Converts byte array to hex string.

```javascript
var hex = utils.bytesToHex([0x12, 0x34]);
// hex = "0x1234"
```

#### utils.keccak256(data)

Computes Keccak-256 hash.

```javascript
var hash = utils.keccak256("hello");
var hash2 = utils.keccak256("0x68656c6c6f"); // Same as above
```

#### utils.getFunctionSelector(signature)

Computes 4-byte function selector from signature.

```javascript
var selector = utils.getFunctionSelector("transfer(address,uint256)");
// selector = "0xa9059cbb"
```

#### utils.encodeAddress(address)

Pads address to 32 bytes (ABI encoding).

```javascript
var encoded = utils.encodeAddress("0x1234567890123456789012345678901234567890");
// encoded = "0x0000000000000000000000001234567890123456789012345678901234567890"
```

#### utils.encodeUint256(value)

Encodes number as 32-byte uint256.

```javascript
var encoded = utils.encodeUint256(1000);
var encoded2 = utils.encodeUint256("0x3e8"); // Same value
```

#### utils.encodePacked(...args)

Performs packed ABI encoding (concatenation without padding).

```javascript
var data = utils.encodePacked(
    "0xa9059cbb",  // function selector
    "0x0000000000000000000000001234567890123456789012345678901234567890", // address
    utils.encodeUint256(1000) // amount
);
```

#### utils.parseJSON(string)

Parses JSON string.

```javascript
var obj = utils.parseJSON('{"key": "value"}');
```

#### utils.stringifyJSON(value)

Converts value to JSON string.

```javascript
var json = utils.stringifyJSON({ key: "value" });
```

### console Object

Logging functions that output to RPCGofer's logger.

```javascript
console.log("Info message", data);
console.debug("Debug message");
console.warn("Warning message");
console.error("Error message");
```

## Advanced Examples

### Calling a Smart Contract

```javascript
// @method custom_getTokenBalance
// Get ERC20 token balance for address

function execute(params, upstream) {
    var tokenAddress = params[0];
    var walletAddress = params[1];

    // balanceOf(address) selector
    var selector = utils.getFunctionSelector("balanceOf(address)");
    var encodedAddress = utils.encodeAddress(walletAddress);
    var data = selector + encodedAddress.slice(2); // Remove 0x from encoded address

    var result = upstream.call("eth_call", [{
        to: tokenAddress,
        data: data
    }, "latest"]);

    // Parse uint256 result
    return result;
}
```

### Multi-call Pattern

```javascript
// @method custom_getMultipleBalances
// Get ETH balances for multiple addresses

function execute(params, upstream) {
    var addresses = params;

    var calls = addresses.map(function(addr) {
        return {
            method: "eth_getBalance",
            params: [addr, "latest"]
        };
    });

    var results = upstream.batchCall(calls);

    return results.map(function(balance, i) {
        return {
            address: addresses[i],
            balance: balance
        };
    });
}
```

### Error Handling

```javascript
// @method custom_safeCall
function execute(params, upstream) {
    try {
        var result = upstream.call(params[0], params[1] || []);
        return { success: true, result: result };
    } catch (e) {
        return { success: false, error: e.message };
    }
}
```

## Error Codes

Plugin-specific JSON-RPC error codes:

| Code | Meaning |
|------|---------|
| -32001 | Plugin not found |
| -32002 | Plugin execution error |
| -32003 | Plugin timeout |
| -32004 | Invalid plugin arguments |

## Best Practices

1. **Validate Input** - Always validate `params` before processing
2. **Use Batch Calls** - Prefer `batchCall` over multiple `call` when possible
3. **Handle Errors** - Use try/catch for graceful error handling
4. **Keep It Simple** - Complex logic may hit timeout limits
5. **Use Descriptive Names** - Prefix custom methods (e.g., `custom_`, `app_`)
6. **Log Sparingly** - Excessive logging impacts performance

## Limitations

- **No async/await** - goja does not support native async/await
- **No external imports** - Cannot import npm modules
- **Timeout** - Long-running plugins will be terminated
- **Memory** - No explicit memory limits, but complex operations may be slow
- **ES5 Syntax** - Use `var` instead of `let`/`const`, use `function` instead of arrow functions

## Debugging

Enable debug logging to see plugin execution:

```json
{
  "logLevel": "debug"
}
```

Plugin logs appear with `[plugin]` prefix:

```
2024-01-15T10:30:00Z DBG executing plugin method=custom_isContract
2024-01-15T10:30:00Z INF [plugin] [Processing 5 addresses]
```
