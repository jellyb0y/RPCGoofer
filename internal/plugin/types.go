package plugin

import (
	"context"
	"encoding/json"

	"github.com/dop251/goja"

	"rpcgofer/internal/jsonrpc"
)

// Plugin represents a loaded JavaScript plugin
type Plugin struct {
	Name   string        // plugin name (filename without extension)
	Method string        // RPC method this plugin handles
	Script string        // JavaScript source code
	VM     *goja.Runtime // goja VM instance
}

// Manager defines the plugin manager interface
type Manager interface {
	// HasPlugin checks if a plugin exists for the given method
	HasPlugin(method string) bool
	// Execute runs the plugin for the given method
	Execute(ctx context.Context, method string, id jsonrpc.ID, params json.RawMessage, caller UpstreamCaller) *jsonrpc.Response
	// GetMethods returns all registered plugin methods
	GetMethods() []string
	// Close releases all resources
	Close()
}

// UpstreamCaller provides methods to call upstream RPC endpoints from plugins
type UpstreamCaller interface {
	// Call executes a single RPC call to upstream
	Call(method string, params interface{}) (json.RawMessage, error)
	// BatchCall executes multiple RPC calls to upstream
	BatchCall(calls []CallRequest) ([]json.RawMessage, error)
}

// CallRequest represents a single RPC call in a batch
type CallRequest struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

// JSCallRequest is the JavaScript representation of CallRequest
type JSCallRequest struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

// PluginError represents an error that occurred during plugin execution
type PluginError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error implements the error interface
func (e *PluginError) Error() string {
	return e.Message
}

// NewPluginError creates a new plugin error
func NewPluginError(code int, message string) *PluginError {
	return &PluginError{
		Code:    code,
		Message: message,
	}
}

// Plugin error codes
const (
	ErrCodePluginNotFound    = -32001
	ErrCodePluginExecution   = -32002
	ErrCodePluginTimeout     = -32003
	ErrCodePluginInvalidArgs = -32004
)
