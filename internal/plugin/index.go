package plugin

// Package plugin provides JavaScript plugin support for custom RPC methods.
//
// Plugins are JavaScript files loaded from a directory at startup.
// Each plugin must define:
//   - A @method directive specifying the RPC method name
//   - An execute(params, upstream) function
//
// Example plugin:
//
//	// @method custom_isContract
//	function execute(params, upstream) {
//	    var addresses = params[0];
//	    var calls = addresses.map(function(addr) {
//	        return { method: "eth_getCode", params: [addr, "latest"] };
//	    });
//	    var results = upstream.batchCall(calls);
//	    return results.map(function(code) {
//	        return code !== "0x" && code !== null;
//	    });
//	}
