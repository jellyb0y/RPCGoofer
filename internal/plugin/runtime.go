package plugin

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dop251/goja"
	"github.com/rs/zerolog"

	"golang.org/x/crypto/sha3"
)

// Runtime wraps goja VM with plugin-specific bindings
type Runtime struct {
	vm     *goja.Runtime
	logger zerolog.Logger
}

// NewRuntime creates a new Runtime with all necessary bindings
func NewRuntime(logger zerolog.Logger) *Runtime {
	vm := goja.New()
	r := &Runtime{
		vm:     vm,
		logger: logger,
	}
	r.setupBindings()
	return r
}

// VM returns the underlying goja runtime
func (r *Runtime) VM() *goja.Runtime {
	return r.vm
}

// setupBindings sets up all JavaScript bindings
func (r *Runtime) setupBindings() {
	r.setupConsole()
	r.setupUtils()
}

// setupConsole creates console.log and console.error bindings
func (r *Runtime) setupConsole() {
	console := r.vm.NewObject()

	console.Set("log", func(call goja.FunctionCall) goja.Value {
		args := make([]interface{}, len(call.Arguments))
		for i, arg := range call.Arguments {
			args[i] = arg.Export()
		}
		r.logger.Info().Msgf("[plugin] %v", args)
		return goja.Undefined()
	})

	console.Set("error", func(call goja.FunctionCall) goja.Value {
		args := make([]interface{}, len(call.Arguments))
		for i, arg := range call.Arguments {
			args[i] = arg.Export()
		}
		r.logger.Error().Msgf("[plugin] %v", args)
		return goja.Undefined()
	})

	console.Set("warn", func(call goja.FunctionCall) goja.Value {
		args := make([]interface{}, len(call.Arguments))
		for i, arg := range call.Arguments {
			args[i] = arg.Export()
		}
		r.logger.Warn().Msgf("[plugin] %v", args)
		return goja.Undefined()
	})

	console.Set("debug", func(call goja.FunctionCall) goja.Value {
		args := make([]interface{}, len(call.Arguments))
		for i, arg := range call.Arguments {
			args[i] = arg.Export()
		}
		r.logger.Debug().Msgf("[plugin] %v", args)
		return goja.Undefined()
	})

	r.vm.Set("console", console)
}

// setupUtils creates utility functions for blockchain operations
func (r *Runtime) setupUtils() {
	utils := r.vm.NewObject()

	// hexToBytes converts hex string to byte array
	utils.Set("hexToBytes", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("hexToBytes requires 1 argument"))
		}
		hexStr := call.Arguments[0].String()
		hexStr = strings.TrimPrefix(hexStr, "0x")
		bytes, err := hex.DecodeString(hexStr)
		if err != nil {
			panic(r.vm.ToValue(fmt.Sprintf("invalid hex string: %v", err)))
		}
		return r.vm.ToValue(bytes)
	})

	// bytesToHex converts byte array to hex string
	utils.Set("bytesToHex", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("bytesToHex requires 1 argument"))
		}
		exported := call.Arguments[0].Export()
		var bytes []byte
		switch v := exported.(type) {
		case []byte:
			bytes = v
		case []interface{}:
			bytes = make([]byte, len(v))
			for i, b := range v {
				if num, ok := b.(int64); ok {
					bytes[i] = byte(num)
				} else if num, ok := b.(float64); ok {
					bytes[i] = byte(num)
				}
			}
		default:
			panic(r.vm.ToValue("bytesToHex requires byte array"))
		}
		return r.vm.ToValue("0x" + hex.EncodeToString(bytes))
	})

	// keccak256 computes keccak256 hash
	utils.Set("keccak256", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("keccak256 requires 1 argument"))
		}
		var data []byte
		exported := call.Arguments[0].Export()
		switch v := exported.(type) {
		case string:
			// If hex string, decode it
			if strings.HasPrefix(v, "0x") {
				var err error
				data, err = hex.DecodeString(strings.TrimPrefix(v, "0x"))
				if err != nil {
					panic(r.vm.ToValue(fmt.Sprintf("invalid hex string: %v", err)))
				}
			} else {
				data = []byte(v)
			}
		case []byte:
			data = v
		case []interface{}:
			data = make([]byte, len(v))
			for i, b := range v {
				if num, ok := b.(int64); ok {
					data[i] = byte(num)
				} else if num, ok := b.(float64); ok {
					data[i] = byte(num)
				}
			}
		default:
			panic(r.vm.ToValue("keccak256 requires string or byte array"))
		}

		hash := sha3.NewLegacyKeccak256()
		hash.Write(data)
		result := hash.Sum(nil)
		return r.vm.ToValue("0x" + hex.EncodeToString(result))
	})

	// encodePacked performs simple ABI encoding (packed)
	utils.Set("encodePacked", func(call goja.FunctionCall) goja.Value {
		var result []byte
		for _, arg := range call.Arguments {
			exported := arg.Export()
			switch v := exported.(type) {
			case string:
				if strings.HasPrefix(v, "0x") {
					bytes, err := hex.DecodeString(strings.TrimPrefix(v, "0x"))
					if err == nil {
						result = append(result, bytes...)
					}
				} else {
					result = append(result, []byte(v)...)
				}
			case int64:
				// Encode as 32 bytes
				buf := make([]byte, 32)
				for i := 0; i < 8; i++ {
					buf[31-i] = byte(v >> (8 * i))
				}
				result = append(result, buf...)
			case float64:
				intVal := int64(v)
				buf := make([]byte, 32)
				for i := 0; i < 8; i++ {
					buf[31-i] = byte(intVal >> (8 * i))
				}
				result = append(result, buf...)
			case []byte:
				result = append(result, v...)
			}
		}
		return r.vm.ToValue("0x" + hex.EncodeToString(result))
	})

	// getFunctionSelector computes 4-byte function selector from signature
	utils.Set("getFunctionSelector", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("getFunctionSelector requires function signature"))
		}
		signature := call.Arguments[0].String()
		hash := sha3.NewLegacyKeccak256()
		hash.Write([]byte(signature))
		result := hash.Sum(nil)[:4]
		return r.vm.ToValue("0x" + hex.EncodeToString(result))
	})

	// encodeAddress pads address to 32 bytes
	utils.Set("encodeAddress", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("encodeAddress requires address"))
		}
		addr := call.Arguments[0].String()
		addr = strings.TrimPrefix(addr, "0x")
		if len(addr) != 40 {
			panic(r.vm.ToValue("invalid address length"))
		}
		// Pad to 32 bytes (64 hex chars)
		padded := strings.Repeat("0", 24) + addr
		return r.vm.ToValue("0x" + padded)
	})

	// encodeUint256 encodes uint256 to 32 bytes
	utils.Set("encodeUint256", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("encodeUint256 requires number"))
		}
		exported := call.Arguments[0].Export()
		var intVal int64
		switch v := exported.(type) {
		case int64:
			intVal = v
		case float64:
			intVal = int64(v)
		case string:
			// Parse hex or decimal
			if strings.HasPrefix(v, "0x") {
				_, err := fmt.Sscanf(v, "0x%x", &intVal)
				if err != nil {
					panic(r.vm.ToValue(fmt.Sprintf("invalid hex number: %v", err)))
				}
			} else {
				_, err := fmt.Sscanf(v, "%d", &intVal)
				if err != nil {
					panic(r.vm.ToValue(fmt.Sprintf("invalid number: %v", err)))
				}
			}
		default:
			panic(r.vm.ToValue("encodeUint256 requires number or string"))
		}
		buf := make([]byte, 32)
		for i := 0; i < 8; i++ {
			buf[31-i] = byte(intVal >> (8 * i))
		}
		return r.vm.ToValue("0x" + hex.EncodeToString(buf))
	})

	// parseJSON parses JSON string
	utils.Set("parseJSON", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("parseJSON requires string"))
		}
		jsonStr := call.Arguments[0].String()
		var result interface{}
		if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
			panic(r.vm.ToValue(fmt.Sprintf("invalid JSON: %v", err)))
		}
		return r.vm.ToValue(result)
	})

	// stringifyJSON converts value to JSON string
	utils.Set("stringifyJSON", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("stringifyJSON requires value"))
		}
		exported := call.Arguments[0].Export()
		data, err := json.Marshal(exported)
		if err != nil {
			panic(r.vm.ToValue(fmt.Sprintf("JSON stringify error: %v", err)))
		}
		return r.vm.ToValue(string(data))
	})

	r.vm.Set("utils", utils)
}

// SetupUpstreamCaller creates upstream object with call methods
func (r *Runtime) SetupUpstreamCaller(caller UpstreamCaller) {
	upstream := r.vm.NewObject()

	// call executes single RPC call
	upstream.Set("call", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 2 {
			panic(r.vm.ToValue("upstream.call requires method and params"))
		}
		method := call.Arguments[0].String()
		params := call.Arguments[1].Export()

		result, err := caller.Call(method, params)
		if err != nil {
			panic(r.vm.ToValue(fmt.Sprintf("upstream call failed: %v", err)))
		}

		var parsed interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			return r.vm.ToValue(string(result))
		}
		return r.vm.ToValue(parsed)
	})

	// batchCall executes multiple RPC calls
	upstream.Set("batchCall", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(r.vm.ToValue("upstream.batchCall requires array of calls"))
		}

		exported := call.Arguments[0].Export()
		callsSlice, ok := exported.([]interface{})
		if !ok {
			panic(r.vm.ToValue("upstream.batchCall requires array"))
		}

		calls := make([]CallRequest, 0, len(callsSlice))
		for _, c := range callsSlice {
			callMap, ok := c.(map[string]interface{})
			if !ok {
				panic(r.vm.ToValue("each call must be object with method and params"))
			}
			method, _ := callMap["method"].(string)
			params := callMap["params"]
			calls = append(calls, CallRequest{
				Method: method,
				Params: params,
			})
		}

		results, err := caller.BatchCall(calls)
		if err != nil {
			panic(r.vm.ToValue(fmt.Sprintf("upstream batch call failed: %v", err)))
		}

		parsed := make([]interface{}, len(results))
		for i, result := range results {
			var v interface{}
			if err := json.Unmarshal(result, &v); err != nil {
				parsed[i] = string(result)
			} else {
				parsed[i] = v
			}
		}
		return r.vm.ToValue(parsed)
	})

	r.vm.Set("upstream", upstream)
}

// RunScript executes JavaScript code and returns the result
func (r *Runtime) RunScript(script string) (goja.Value, error) {
	return r.vm.RunString(script)
}

// CallFunction calls a JavaScript function by name
func (r *Runtime) CallFunction(name string, args ...interface{}) (goja.Value, error) {
	fn, ok := goja.AssertFunction(r.vm.Get(name))
	if !ok {
		return nil, fmt.Errorf("function %s not found", name)
	}

	jsArgs := make([]goja.Value, len(args))
	for i, arg := range args {
		jsArgs[i] = r.vm.ToValue(arg)
	}

	return fn(goja.Undefined(), jsArgs...)
}
