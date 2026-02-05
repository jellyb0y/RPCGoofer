package blockparam

import (
	"encoding/json"
	"strconv"
	"strings"
)

// DynamicBlockTags contains block tags that indicate dynamic/latest data
var DynamicBlockTags = map[string]bool{
	"latest":    true,
	"pending":   true,
	"earliest":  true,
	"safe":      true,
	"finalized": true,
}

// GetBlockParamIndex returns the index of block parameter for each method
func GetBlockParamIndex(method string) int {
	switch method {
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
	case "debug_traceBlockByNumber":
		return 0
	case "debug_traceCall":
		return 1
	case "trace_call":
		return 1
	case "trace_callMany":
		return 1
	case "trace_replayBlockTransactions":
		return 0
	default:
		return -1
	}
}

// IsDynamicBlockParam checks if a single param is a dynamic block tag
func IsDynamicBlockParam(param json.RawMessage) bool {
	var strParam string
	if err := json.Unmarshal(param, &strParam); err != nil {
		var objParam map[string]interface{}
		if err := json.Unmarshal(param, &objParam); err != nil {
			return true
		}
		if blockNum, ok := objParam["blockNumber"]; ok {
			if strBlockNum, ok := blockNum.(string); ok {
				return DynamicBlockTags[strings.ToLower(strBlockNum)]
			}
		}
		return false
	}
	return DynamicBlockTags[strings.ToLower(strParam)]
}

// parseHexUint64 parses a hex string (with 0x prefix) to uint64
func parseHexUint64(hexStr string) (uint64, error) {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	return strconv.ParseUint(hexStr, 16, 64)
}

// parseBlockNumberFromParam parses a single param as block number (string hex or object with blockNumber)
// Returns (blockNum, true) if it's a concrete number, (0, false) if dynamic tag or parse error
func parseBlockNumberFromParam(param json.RawMessage) (uint64, bool) {
	if IsDynamicBlockParam(param) {
		return 0, false
	}
	var strParam string
	if err := json.Unmarshal(param, &strParam); err != nil {
		var objParam map[string]interface{}
		if err := json.Unmarshal(param, &objParam); err != nil {
			return 0, false
		}
		if blockNum, ok := objParam["blockNumber"]; ok {
			if strBlockNum, ok := blockNum.(string); ok {
				n, err := parseHexUint64(strBlockNum)
				if err != nil {
					return 0, false
				}
				return n, true
			}
		}
		return 0, false
	}
	n, err := parseHexUint64(strParam)
	if err != nil {
		return 0, false
	}
	return n, true
}

// GetRequestedBlockNumber returns the block number requested by the call when it is a concrete number.
// For single-block methods: the block param; for eth_getLogs/trace_filter: max(fromBlock, toBlock).
// Returns (0, false) when the method has no block param, uses a dynamic tag (latest/pending/etc), or parse fails.
func GetRequestedBlockNumber(method string, params json.RawMessage) (uint64, bool) {
	if params == nil || len(params) == 0 {
		return 0, false
	}

	switch method {
	case "eth_getLogs", "trace_filter":
		return getRequestedBlockNumberRange(params)
	default:
		return getRequestedBlockNumberSingle(method, params)
	}
}

func getRequestedBlockNumberSingle(method string, params json.RawMessage) (uint64, bool) {
	var paramsArray []json.RawMessage
	if err := json.Unmarshal(params, &paramsArray); err != nil {
		return 0, false
	}
	idx := GetBlockParamIndex(method)
	if idx < 0 || idx >= len(paramsArray) {
		return 0, false
	}
	return parseBlockNumberFromParam(paramsArray[idx])
}

func getRequestedBlockNumberRange(params json.RawMessage) (uint64, bool) {
	var paramsArray []json.RawMessage
	if err := json.Unmarshal(params, &paramsArray); err != nil || len(paramsArray) == 0 {
		return 0, false
	}
	var filterObj map[string]interface{}
	if err := json.Unmarshal(paramsArray[0], &filterObj); err != nil {
		return 0, false
	}
	var fromBlock, toBlock uint64
	fromOK := false
	toOK := false
	if v, ok := filterObj["fromBlock"]; ok {
		if s, ok := v.(string); ok {
			if DynamicBlockTags[strings.ToLower(s)] {
				return 0, false
			}
			n, err := parseHexUint64(s)
			if err == nil {
				fromBlock = n
				fromOK = true
			}
		}
	}
	if v, ok := filterObj["toBlock"]; ok {
		if s, ok := v.(string); ok {
			if DynamicBlockTags[strings.ToLower(s)] {
				return 0, false
			}
			n, err := parseHexUint64(s)
			if err == nil {
				toBlock = n
				toOK = true
			}
		}
	}
	if !fromOK && !toOK {
		return 0, false
	}
	maxBlock := fromBlock
	if toBlock > maxBlock {
		maxBlock = toBlock
	}
	return maxBlock, true
}
