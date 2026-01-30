// @method custom_isContract
// Checks if addresses are contracts using batch eth_getCode calls
// 
// Request: {"jsonrpc":"2.0","method":"custom_isContract","params":["0x...", "0x..."],"id":1}
// Response: {"jsonrpc":"2.0","result":[true, false, ...],"id":1}

function execute(params, upstream) {
    // params is directly the array of addresses
    if (!params || !Array.isArray(params)) {
        throw new Error("params must be array of addresses");
    }

    if (params.length === 0) {
        return [];
    }

    // Validate addresses format
    for (var i = 0; i < params.length; i++) {
        var addr = params[i];
        if (typeof addr !== "string" || !addr.match(/^0x[a-fA-F0-9]{40}$/)) {
            throw new Error("invalid address at index " + i + ": " + addr);
        }
    }

    // Build batch of eth_getCode calls
    var calls = params.map(function(addr) {
        return { 
            method: "eth_getCode", 
            params: [addr, "latest"] 
        };
    });

    // Execute batch call
    var results = upstream.batchCall(calls);

    // Convert results to boolean array
    // Code "0x" or empty means EOA (not a contract)
    return results.map(function(code) {
        if (code === null || code === undefined) {
            return false;
        }
        if (typeof code === "object" && code.error) {
            return false;
        }
        // Contract has code length > 2 ("0x" prefix only = no code)
        return typeof code === "string" && code.length > 2;
    });
}
