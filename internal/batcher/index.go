package batcher

// Package batcher provides request batching (coalescing) for RPC methods.
//
// Methods that accept array parameters can be configured for batching.
// Multiple concurrent requests are aggregated into a single batch request,
// executed once, and results are distributed back to each caller.
//
// Example configuration:
//
//	{
//	  "batching": {
//	    "enabled": true,
//	    "methods": {
//	      "custom_isContract": {
//	        "maxSize": 100,
//	        "maxWait": 500,
//	        "aggregateParam": 0
//	      }
//	    }
//	  }
//	}
