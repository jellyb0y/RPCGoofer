package plugin

import (
	"encoding/json"
	"testing"
)

func TestRuntime_ConsoleBindings(t *testing.T) {
	rt := NewRuntime(testLogger())

	// console.log should not panic
	_, err := rt.RunScript(`console.log("test message");`)
	if err != nil {
		t.Fatalf("console.log failed: %v", err)
	}

	// console.error should not panic
	_, err = rt.RunScript(`console.error("test error");`)
	if err != nil {
		t.Fatalf("console.error failed: %v", err)
	}

	// console.warn should not panic
	_, err = rt.RunScript(`console.warn("test warn");`)
	if err != nil {
		t.Fatalf("console.warn failed: %v", err)
	}

	// console.debug should not panic
	_, err = rt.RunScript(`console.debug("test debug");`)
	if err != nil {
		t.Fatalf("console.debug failed: %v", err)
	}
}

func TestRuntime_UtilsFunctionSelector(t *testing.T) {
	rt := NewRuntime(testLogger())

	// getNativeBalances(address[]) selector = 0x46eb2535
	val, err := rt.RunScript(`utils.getFunctionSelector("getNativeBalances(address[])")`)
	if err != nil {
		t.Fatalf("getFunctionSelector failed: %v", err)
	}
	got := val.String()
	want := "0x4c04bf99"
	if got != want {
		t.Errorf("getFunctionSelector = %q, want %q", got, want)
	}
}

func TestRuntime_UtilsEncodeAddress(t *testing.T) {
	rt := NewRuntime(testLogger())

	val, err := rt.RunScript(`utils.encodeAddress("0x1234567890abcdef1234567890abcdef12345678")`)
	if err != nil {
		t.Fatalf("encodeAddress failed: %v", err)
	}
	got := val.String()
	// 20-byte address padded to 32 bytes = 24 zeros + 40 hex chars
	want := "0x0000000000000000000000001234567890abcdef1234567890abcdef12345678"
	if got != want {
		t.Errorf("encodeAddress = %q, want %q", got, want)
	}
}

func TestRuntime_SetupUpstreamCaller_Call(t *testing.T) {
	rt := NewRuntime(testLogger())

	called := false
	caller := &mockUpstreamCaller{
		callFn: func(method string, params interface{}) (json.RawMessage, error) {
			called = true
			if method != "eth_blockNumber" {
				t.Errorf("unexpected method: %s", method)
			}
			return json.RawMessage(`"0x10"`), nil
		},
	}
	rt.SetupUpstreamCaller(caller)

	val, err := rt.RunScript(`upstream.call("eth_blockNumber", [])`)
	if err != nil {
		t.Fatalf("upstream.call failed: %v", err)
	}
	if !called {
		t.Error("upstream.call did not invoke mock")
	}
	if val.String() != "0x10" {
		t.Errorf("upstream.call result = %q, want %q", val.String(), "0x10")
	}
}

func TestRuntime_SetupUpstreamCaller_BatchCall(t *testing.T) {
	rt := NewRuntime(testLogger())

	called := false
	caller := &mockUpstreamCaller{
		batchCallFn: func(calls []CallRequest) ([]json.RawMessage, error) {
			called = true
			if len(calls) != 2 {
				t.Errorf("expected 2 calls, got %d", len(calls))
			}
			return []json.RawMessage{
				json.RawMessage(`"0xa"`),
				json.RawMessage(`"0xb"`),
			}, nil
		},
	}
	rt.SetupUpstreamCaller(caller)

	val, err := rt.RunScript(`
		var results = upstream.batchCall([
			{method: "eth_getCode", params: ["0x1", "latest"]},
			{method: "eth_getCode", params: ["0x2", "latest"]}
		]);
		results.length;
	`)
	if err != nil {
		t.Fatalf("upstream.batchCall failed: %v", err)
	}
	if !called {
		t.Error("upstream.batchCall did not invoke mock")
	}
	if val.ToInteger() != 2 {
		t.Errorf("batchCall returned %d results, want 2", val.ToInteger())
	}
}

func TestRuntime_SetConfig_Nil(t *testing.T) {
	rt := NewRuntime(testLogger())
	rt.SetConfig(nil)

	// config should be an object, not undefined
	val, err := rt.RunScript(`typeof config`)
	if err != nil {
		t.Fatalf("RunScript failed: %v", err)
	}
	if val.String() != "object" {
		t.Errorf("typeof config = %q, want %q", val.String(), "object")
	}

	// Accessing missing key should return undefined, not throw
	val, err = rt.RunScript(`typeof config.codeCheckerAddress`)
	if err != nil {
		t.Fatalf("accessing missing key failed: %v", err)
	}
	if val.String() != "undefined" {
		t.Errorf("typeof config.codeCheckerAddress = %q, want %q", val.String(), "undefined")
	}
}

func TestRuntime_SetConfig_WithValues(t *testing.T) {
	rt := NewRuntime(testLogger())
	rt.SetConfig(map[string]interface{}{
		"codeCheckerAddress": "0xABC123",
	})

	val, err := rt.RunScript(`config.codeCheckerAddress`)
	if err != nil {
		t.Fatalf("RunScript failed: %v", err)
	}
	if val.String() != "0xABC123" {
		t.Errorf("config.codeCheckerAddress = %q, want %q", val.String(), "0xABC123")
	}
}

func TestRuntime_SetConfig_Empty(t *testing.T) {
	rt := NewRuntime(testLogger())
	rt.SetConfig(map[string]interface{}{})

	val, err := rt.RunScript(`typeof config.codeCheckerAddress`)
	if err != nil {
		t.Fatalf("RunScript failed: %v", err)
	}
	if val.String() != "undefined" {
		t.Errorf("typeof config.codeCheckerAddress = %q, want %q", val.String(), "undefined")
	}
}
