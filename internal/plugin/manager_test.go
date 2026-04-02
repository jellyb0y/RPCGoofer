package plugin

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"rpcgofer/internal/jsonrpc"
)

func TestPluginManager_LoadFromDirectory_ValidPlugin(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "echo.js", `// @method custom_echo
function execute(params, upstream) {
    return params;
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatalf("LoadFromDirectory failed: %v", err)
	}

	if !pm.HasPlugin("custom_echo") {
		t.Error("expected HasPlugin('custom_echo') to be true")
	}

	methods := pm.GetMethods()
	if len(methods) != 1 || methods[0] != "custom_echo" {
		t.Errorf("GetMethods() = %v, want [custom_echo]", methods)
	}
}

func TestPluginManager_LoadFromDirectory_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatalf("LoadFromDirectory on empty dir failed: %v", err)
	}

	if len(pm.GetMethods()) != 0 {
		t.Error("expected no methods from empty directory")
	}
}

func TestPluginManager_LoadFromDirectory_NonExistentDir(t *testing.T) {
	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory("/nonexistent/path/to/plugins"); err != nil {
		t.Fatalf("expected no error for non-existent dir, got: %v", err)
	}
}

func TestPluginManager_LoadFromDirectory_NoMethodDirective(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "bad.js", `function execute(params) { return params; }`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatalf("LoadFromDirectory failed: %v", err)
	}

	// Plugin without @method should be skipped, not cause error
	if len(pm.GetMethods()) != 0 {
		t.Error("expected no methods when @method directive is missing")
	}
}

func TestPluginManager_Execute_NotFound(t *testing.T) {
	pm := NewPluginManager(testLogger())
	caller := &mockUpstreamCaller{}

	resp := pm.Execute(context.Background(), "test", "nonexistent_method", jsonrpc.NewIDInt(1), nil, caller)

	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != ErrCodePluginNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodePluginNotFound)
	}
}

func TestPluginManager_Execute_SimplePlugin(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "echo.js", `// @method custom_echo
function execute(params, upstream) {
    return params;
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	params := json.RawMessage(`["hello", "world"]`)
	caller := &mockUpstreamCaller{}

	resp := pm.Execute(context.Background(), "test", "custom_echo", jsonrpc.NewIDInt(1), params, caller)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result []string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if len(result) != 2 || result[0] != "hello" || result[1] != "world" {
		t.Errorf("result = %v, want [hello, world]", result)
	}
}

func TestPluginManager_Execute_Timeout(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "slow.js", `// @method custom_slow
function execute(params, upstream) {
    while(true) {}
    return null;
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	pm.SetTimeout(100 * time.Millisecond)
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	caller := &mockUpstreamCaller{}
	resp := pm.Execute(context.Background(), "test", "custom_slow", jsonrpc.NewIDInt(1), nil, caller)

	if resp.Error == nil {
		t.Fatal("expected timeout error")
	}
	if resp.Error.Code != ErrCodePluginTimeout {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodePluginTimeout)
	}
}

func TestPluginManager_Execute_UpstreamCaller(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "caller.js", `// @method custom_caller
function execute(params, upstream) {
    var result = upstream.call("eth_blockNumber", []);
    return result;
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	caller := &mockUpstreamCaller{
		callFn: func(method string, params interface{}) (json.RawMessage, error) {
			return json.RawMessage(`"0x1a2b3c"`), nil
		},
	}

	resp := pm.Execute(context.Background(), "test", "custom_caller", jsonrpc.NewIDInt(1), json.RawMessage(`[]`), caller)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if result != "0x1a2b3c" {
		t.Errorf("result = %q, want %q", result, "0x1a2b3c")
	}
}

func TestPluginManager_Close(t *testing.T) {
	dir := t.TempDir()
	_ = writeTestPlugin(dir, "echo.js", `// @method custom_echo
function execute(params, upstream) { return params; }`)

	pm := NewPluginManager(testLogger())
	_ = pm.LoadFromDirectory(dir)

	pm.Close()

	if pm.HasPlugin("custom_echo") {
		t.Error("expected no plugins after Close()")
	}
}

func TestPluginManager_Execute_NoExecuteFunction(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "nofunc.js", `// @method custom_nofunc
var x = 42;`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	caller := &mockUpstreamCaller{}
	resp := pm.Execute(context.Background(), "test", "custom_nofunc", jsonrpc.NewIDInt(1), nil, caller)

	if resp.Error == nil {
		t.Fatal("expected error for missing execute function")
	}
	if resp.Error.Code != ErrCodePluginExecution {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodePluginExecution)
	}
}

func TestPluginManager_LoadFromDirectory_SkipsNonJS(t *testing.T) {
	dir := t.TempDir()
	// Write a non-JS file
	if err := os.WriteFile(dir+"/readme.txt", []byte("not a plugin"), 0644); err != nil {
		t.Fatal(err)
	}
	// Write a valid JS plugin
	_ = writeTestPlugin(dir, "echo.js", `// @method custom_echo
function execute(params, upstream) { return params; }`)

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	methods := pm.GetMethods()
	if len(methods) != 1 {
		t.Errorf("expected 1 method, got %d", len(methods))
	}
}

func TestPluginManager_SetGroupConfig(t *testing.T) {
	pm := NewPluginManager(testLogger())

	pm.SetGroupConfig("base", map[string]interface{}{
		"codeCheckerAddress": "0xBASE",
	})
	pm.SetGroupConfig("bsc", map[string]interface{}{
		"codeCheckerAddress": "0xBSC",
	})

	pm.mu.RLock()
	baseCfg := pm.groupConfigs["base"]
	bscCfg := pm.groupConfigs["bsc"]
	pm.mu.RUnlock()

	if baseCfg["codeCheckerAddress"] != "0xBASE" {
		t.Errorf("base config = %v, want codeCheckerAddress=0xBASE", baseCfg)
	}
	if bscCfg["codeCheckerAddress"] != "0xBSC" {
		t.Errorf("bsc config = %v, want codeCheckerAddress=0xBSC", bscCfg)
	}
}

func TestPluginManager_SetGroupConfig_Concurrent(t *testing.T) {
	pm := NewPluginManager(testLogger())

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(n int) {
			pm.SetGroupConfig("group", map[string]interface{}{
				"key": n,
			})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// If we get here without race detector complaint, test passes
}

func TestPluginManager_Execute_InjectsConfigForGroup(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "readconfig.js", `// @method custom_readconfig
function execute(params, upstream) {
    return config.codeCheckerAddress;
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	pm.SetGroupConfig("base", map[string]interface{}{
		"codeCheckerAddress": "0xBASEADDRESS",
	})

	caller := &mockUpstreamCaller{}
	resp := pm.Execute(context.Background(), "base", "custom_readconfig", jsonrpc.NewIDInt(1), nil, caller)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result != "0xBASEADDRESS" {
		t.Errorf("result = %q, want %q", result, "0xBASEADDRESS")
	}
}

func TestPluginManager_Execute_NoConfigForGroup(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "checkconfig.js", `// @method custom_checkconfig
function execute(params, upstream) {
    return typeof config;
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	caller := &mockUpstreamCaller{}
	// Execute with a group that has no config set
	resp := pm.Execute(context.Background(), "unknown_group", "custom_checkconfig", jsonrpc.NewIDInt(1), nil, caller)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result != "object" {
		t.Errorf("typeof config = %q, want %q (should be empty object, not undefined)", result, "object")
	}
}

func TestPluginManager_Execute_DifferentGroups(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "readaddr.js", `// @method custom_readaddr
function execute(params, upstream) {
    return config.codeCheckerAddress;
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	pm.SetGroupConfig("base", map[string]interface{}{
		"codeCheckerAddress": "0xBASE",
	})
	pm.SetGroupConfig("bsc", map[string]interface{}{
		"codeCheckerAddress": "0xBSC",
	})

	caller := &mockUpstreamCaller{}

	// Execute with "base" group
	resp1 := pm.Execute(context.Background(), "base", "custom_readaddr", jsonrpc.NewIDInt(1), nil, caller)
	if resp1.Error != nil {
		t.Fatalf("base error: %v", resp1.Error)
	}
	var result1 string
	json.Unmarshal(resp1.Result, &result1)
	if result1 != "0xBASE" {
		t.Errorf("base result = %q, want %q", result1, "0xBASE")
	}

	// Execute with "bsc" group
	resp2 := pm.Execute(context.Background(), "bsc", "custom_readaddr", jsonrpc.NewIDInt(1), nil, caller)
	if resp2.Error != nil {
		t.Fatalf("bsc error: %v", resp2.Error)
	}
	var result2 string
	json.Unmarshal(resp2.Result, &result2)
	if result2 != "0xBSC" {
		t.Errorf("bsc result = %q, want %q", result2, "0xBSC")
	}
}

func TestPluginManager_Execute_PluginFallbackWhenNoConfig(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "fallback.js", `// @method custom_fallback
function execute(params, upstream) {
    return (config && config.codeCheckerAddress) || "0xDEFAULT";
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	caller := &mockUpstreamCaller{}
	// No SetGroupConfig — config will be empty object
	resp := pm.Execute(context.Background(), "somegroup", "custom_fallback", jsonrpc.NewIDInt(1), nil, caller)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result string
	json.Unmarshal(resp.Result, &result)
	if result != "0xDEFAULT" {
		t.Errorf("result = %q, want %q (fallback should be used)", result, "0xDEFAULT")
	}
}

func TestPluginManager_Execute_PluginUsesConfig(t *testing.T) {
	dir := t.TempDir()
	err := writeTestPlugin(dir, "usecfg.js", `// @method custom_usecfg
function execute(params, upstream) {
    return (config && config.codeCheckerAddress) || "0xDEFAULT";
}`)
	if err != nil {
		t.Fatal(err)
	}

	pm := NewPluginManager(testLogger())
	if err := pm.LoadFromDirectory(dir); err != nil {
		t.Fatal(err)
	}

	pm.SetGroupConfig("base", map[string]interface{}{
		"codeCheckerAddress": "0xFROMCONFIG",
	})

	caller := &mockUpstreamCaller{}
	resp := pm.Execute(context.Background(), "base", "custom_usecfg", jsonrpc.NewIDInt(1), nil, caller)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result string
	json.Unmarshal(resp.Result, &result)
	if result != "0xFROMCONFIG" {
		t.Errorf("result = %q, want %q", result, "0xFROMCONFIG")
	}
}
