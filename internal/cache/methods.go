package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// MethodCacheability defines how a method should be cached
type MethodCacheability int

const (
	// NotCacheable - method should never be cached
	NotCacheable MethodCacheability = iota
	// AlwaysCacheable - method result is immutable and can always be cached
	AlwaysCacheable
	// CacheableWithBlockNumber - cacheable only when block parameter is a specific number (not latest/pending/etc)
	CacheableWithBlockNumber
	// CacheableWithBlockRange - cacheable only when both fromBlock and toBlock are specific numbers
	CacheableWithBlockRange
)

// methodCacheRules maps methods to their cacheability rules
var methodCacheRules = map[string]MethodCacheability{
	// Always cacheable - data is immutable once created
	"eth_getBlockByHash":                    AlwaysCacheable,
	"eth_getTransactionByHash":              AlwaysCacheable,
	"eth_getTransactionReceipt":             AlwaysCacheable,
	"eth_getBlockTransactionCountByHash":    AlwaysCacheable,
	"eth_getTransactionByBlockHashAndIndex": AlwaysCacheable,
	"eth_chainId":                           AlwaysCacheable,
	"net_version":                           AlwaysCacheable,

	// Debug/Trace methods - always cacheable (by hash/tx)
	"debug_traceBlockByHash":  AlwaysCacheable,
	"debug_traceTransaction":  AlwaysCacheable,
	"trace_transaction":       AlwaysCacheable,
	"trace_block":             AlwaysCacheable, // by block hash
	"trace_replayTransaction": AlwaysCacheable,

	// Cacheable with specific block number only
	"eth_getBlockByNumber":                    CacheableWithBlockNumber,
	"eth_getCode":                             CacheableWithBlockNumber,
	"eth_getBalance":                          CacheableWithBlockNumber,
	"eth_getStorageAt":                        CacheableWithBlockNumber,
	"eth_getTransactionCount":                 CacheableWithBlockNumber,
	"eth_call":                                CacheableWithBlockNumber,
	"eth_getBlockTransactionCountByNumber":    CacheableWithBlockNumber,
	"eth_getTransactionByBlockNumberAndIndex": CacheableWithBlockNumber,
	"eth_getBlockReceipts":                    CacheableWithBlockNumber,
	"eth_getProof":                            CacheableWithBlockNumber,

	// Debug/Trace methods - cacheable with specific block number
	"debug_traceBlockByNumber": CacheableWithBlockNumber,
	"debug_traceCall":          CacheableWithBlockNumber,
	"trace_call":               CacheableWithBlockNumber,
	"trace_callMany":           CacheableWithBlockNumber,
	"trace_replayBlockTransactions": CacheableWithBlockNumber,

	// Cacheable with specific block range only
	"eth_getLogs":   CacheableWithBlockRange,
	"trace_filter":  CacheableWithBlockRange,
}

// blockTags that indicate dynamic/latest data - not cacheable
var dynamicBlockTags = map[string]bool{
	"latest":    true,
	"pending":   true,
	"earliest":  true,
	"safe":      true,
	"finalized": true,
}

// disabledMethods holds methods that should not be cached (configured at runtime)
var disabledMethods = make(map[string]bool)

// SetDisabledMethods sets the list of methods that should not be cached
func SetDisabledMethods(methods []string) {
	disabledMethods = make(map[string]bool)
	for _, method := range methods {
		disabledMethods[method] = true
	}
}

// IsMethodDisabled checks if a method is in the disabled list
func IsMethodDisabled(method string) bool {
	return disabledMethods[method]
}

// IsCacheable checks if a request is cacheable based on method and params
func IsCacheable(method string, params json.RawMessage) bool {
	// Check if method is explicitly disabled
	if disabledMethods[method] {
		return false
	}

	rule, exists := methodCacheRules[method]
	if !exists {
		return false
	}

	switch rule {
	case AlwaysCacheable:
		return true
	case CacheableWithBlockNumber:
		return !containsDynamicBlockTag(method, params)
	case CacheableWithBlockRange:
		return !containsDynamicBlockRange(params)
	default:
		return false
	}
}

// containsDynamicBlockTag checks if params contain dynamic block tags
func containsDynamicBlockTag(method string, params json.RawMessage) bool {
	if params == nil || len(params) == 0 {
		return true // No params means default to latest
	}

	var paramsArray []json.RawMessage
	if err := json.Unmarshal(params, &paramsArray); err != nil {
		return true // Cannot parse, assume not cacheable
	}

	// Get the block parameter position based on method
	blockParamIdx := getBlockParamIndex(method)
	if blockParamIdx < 0 || blockParamIdx >= len(paramsArray) {
		// For methods that require block param but don't have it, they default to latest
		if blockParamIdx >= 0 {
			return true
		}
		return false
	}

	return isDynamicBlockParam(paramsArray[blockParamIdx])
}

// getBlockParamIndex returns the index of block parameter for each method
func getBlockParamIndex(method string) int {
	switch method {
	// eth methods
	case "eth_getBlockByNumber":
		return 0
	case "eth_getCode", "eth_getBalance", "eth_getTransactionCount":
		return 1
	case "eth_getStorageAt":
		return 2
	case "eth_call":
		return 1
	case "eth_getBlockTransactionCountByNumber":
		return 0
	case "eth_getTransactionByBlockNumberAndIndex":
		return 0
	case "eth_getBlockReceipts":
		return 0
	case "eth_getProof":
		return 2

	// debug methods
	case "debug_traceBlockByNumber":
		return 0
	case "debug_traceCall":
		return 1 // [callObject, blockNumber, tracerConfig]

	// trace methods (OpenEthereum/Erigon)
	case "trace_call":
		return 1 // [callObject, blockNumber, traceTypes]
	case "trace_callMany":
		return 1 // [calls, blockNumber]
	case "trace_replayBlockTransactions":
		return 0 // [blockNumber, traceTypes]

	default:
		return -1
	}
}

// isDynamicBlockParam checks if a single param is a dynamic block tag
func isDynamicBlockParam(param json.RawMessage) bool {
	var strParam string
	if err := json.Unmarshal(param, &strParam); err != nil {
		// Could be an object (for eth_call block param which can be object or string)
		var objParam map[string]interface{}
		if err := json.Unmarshal(param, &objParam); err != nil {
			return true // Cannot parse, assume dynamic
		}
		// Check for blockNumber field in object
		if blockNum, ok := objParam["blockNumber"]; ok {
			if strBlockNum, ok := blockNum.(string); ok {
				return dynamicBlockTags[strings.ToLower(strBlockNum)]
			}
		}
		return false
	}

	return dynamicBlockTags[strings.ToLower(strParam)]
}

// containsDynamicBlockRange checks if eth_getLogs params contain dynamic block tags
func containsDynamicBlockRange(params json.RawMessage) bool {
	if params == nil || len(params) == 0 {
		return true
	}

	var paramsArray []json.RawMessage
	if err := json.Unmarshal(params, &paramsArray); err != nil {
		return true
	}

	if len(paramsArray) == 0 {
		return true
	}

	var filterObj map[string]interface{}
	if err := json.Unmarshal(paramsArray[0], &filterObj); err != nil {
		return true
	}

	// Check fromBlock
	if fromBlock, ok := filterObj["fromBlock"]; ok {
		if strFromBlock, ok := fromBlock.(string); ok {
			if dynamicBlockTags[strings.ToLower(strFromBlock)] {
				return true
			}
		}
	} else {
		// No fromBlock means default to latest
		return true
	}

	// Check toBlock
	if toBlock, ok := filterObj["toBlock"]; ok {
		if strToBlock, ok := toBlock.(string); ok {
			if dynamicBlockTags[strings.ToLower(strToBlock)] {
				return true
			}
		}
	} else {
		// No toBlock means default to latest
		return true
	}

	return false
}

// GenerateCacheKey creates a unique cache key for a request
func GenerateCacheKey(group, method string, params json.RawMessage) string {
	normalizedParams := normalizeParams(params)
	hash := sha256.Sum256(normalizedParams)
	paramsHash := hex.EncodeToString(hash[:8]) // Use first 8 bytes for shorter key

	return group + ":" + method + ":" + paramsHash
}

// normalizeParams normalizes JSON params for consistent hashing
func normalizeParams(params json.RawMessage) []byte {
	if params == nil || len(params) == 0 {
		return []byte("[]")
	}

	// Try to unmarshal and re-marshal with sorted keys
	var data interface{}
	if err := json.Unmarshal(params, &data); err != nil {
		return params // Return as-is if cannot parse
	}

	normalized := normalizeValue(data)
	result, err := json.Marshal(normalized)
	if err != nil {
		return params
	}

	return result
}

// normalizeValue recursively normalizes a JSON value
func normalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return normalizeMap(val)
	case []interface{}:
		return normalizeArray(val)
	case string:
		return strings.ToLower(val) // Normalize hex addresses/hashes to lowercase
	default:
		return val
	}
}

// normalizeMap normalizes a map by sorting keys
func normalizeMap(m map[string]interface{}) map[string]interface{} {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make(map[string]interface{})
	for _, k := range keys {
		result[k] = normalizeValue(m[k])
	}
	return result
}

// normalizeArray normalizes an array
func normalizeArray(arr []interface{}) []interface{} {
	result := make([]interface{}, len(arr))
	for i, v := range arr {
		result[i] = normalizeValue(v)
	}
	return result
}
