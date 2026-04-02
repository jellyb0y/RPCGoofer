package config

import (
	"encoding/json"
	"testing"
)

func TestGroupConfig_UnmarshalWithoutPluginParams(t *testing.T) {
	data := `{"name": "base", "upstreams": []}`
	var gc GroupConfig
	if err := json.Unmarshal([]byte(data), &gc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if gc.Name != "base" {
		t.Errorf("name = %q, want %q", gc.Name, "base")
	}
	if gc.PluginParams != nil {
		t.Errorf("PluginParams should be nil, got %v", gc.PluginParams)
	}
}

func TestGroupConfig_UnmarshalWithPluginParams(t *testing.T) {
	data := `{"name": "bsc", "upstreams": [], "pluginParams": {"codeCheckerAddress": "0xABC"}}`
	var gc GroupConfig
	if err := json.Unmarshal([]byte(data), &gc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if gc.Name != "bsc" {
		t.Errorf("name = %q, want %q", gc.Name, "bsc")
	}
	if gc.PluginParams == nil {
		t.Fatal("PluginParams should not be nil")
	}
	addr, ok := gc.PluginParams["codeCheckerAddress"].(string)
	if !ok || addr != "0xABC" {
		t.Errorf("codeCheckerAddress = %v, want %q", gc.PluginParams["codeCheckerAddress"], "0xABC")
	}
}

func TestGroupConfig_MarshalWithoutPluginParams(t *testing.T) {
	gc := GroupConfig{Name: "base", Upstreams: []UpstreamConfig{}}
	data, err := json.Marshal(gc)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	// pluginParams should be omitted
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	if _, ok := m["pluginParams"]; ok {
		t.Error("pluginParams should be omitted when nil")
	}
}
