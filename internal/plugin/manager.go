package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/rs/zerolog"

	"rpcgofer/internal/jsonrpc"
)

// DefaultExecutionTimeout is the default timeout for plugin execution
const DefaultExecutionTimeout = 30 * time.Second

// methodDirectiveRegex matches @method directive in comments
var methodDirectiveRegex = regexp.MustCompile(`(?m)^//\s*@method\s+(\S+)`)

// PluginManager manages JavaScript plugins
type PluginManager struct {
	plugins map[string]*Plugin // method -> plugin
	logger  zerolog.Logger
	timeout time.Duration
	mu      sync.RWMutex
}

// NewPluginManager creates a new PluginManager
func NewPluginManager(logger zerolog.Logger) *PluginManager {
	return &PluginManager{
		plugins: make(map[string]*Plugin),
		logger:  logger.With().Str("component", "plugin-manager").Logger(),
		timeout: DefaultExecutionTimeout,
	}
}

// SetTimeout sets the execution timeout for plugins
func (m *PluginManager) SetTimeout(timeout time.Duration) {
	m.timeout = timeout
}

// LoadFromDirectory loads all .js plugins from a directory
func (m *PluginManager) LoadFromDirectory(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if directory exists
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		m.logger.Warn().Str("directory", dir).Msg("plugins directory does not exist")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to stat plugins directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("plugins path is not a directory: %s", dir)
	}

	// Find all .js files
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read plugins directory: %w", err)
	}

	loadedCount := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			continue
		}

		pluginPath := filepath.Join(dir, entry.Name())
		if err := m.loadPlugin(pluginPath); err != nil {
			m.logger.Error().
				Err(err).
				Str("file", entry.Name()).
				Msg("failed to load plugin")
			continue
		}
		loadedCount++
	}

	m.logger.Info().
		Int("loaded", loadedCount).
		Str("directory", dir).
		Msg("plugins loaded")

	return nil
}

// loadPlugin loads a single plugin from a file
func (m *PluginManager) loadPlugin(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read plugin file: %w", err)
	}

	script := string(content)

	// Extract method from @method directive
	method := extractMethodDirective(script)
	if method == "" {
		return fmt.Errorf("plugin missing @method directive")
	}

	// Check for duplicate method
	if _, exists := m.plugins[method]; exists {
		return fmt.Errorf("duplicate method: %s", method)
	}

	// Create plugin entry (VM will be created per-request)
	name := strings.TrimSuffix(filepath.Base(path), ".js")
	plugin := &Plugin{
		Name:   name,
		Method: method,
		Script: script,
	}

	m.plugins[method] = plugin

	m.logger.Info().
		Str("name", name).
		Str("method", method).
		Str("file", filepath.Base(path)).
		Msg("plugin loaded")

	return nil
}

// extractMethodDirective extracts the method name from @method directive
func extractMethodDirective(script string) string {
	matches := methodDirectiveRegex.FindStringSubmatch(script)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// HasPlugin checks if a plugin exists for the given method
func (m *PluginManager) HasPlugin(method string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.plugins[method]
	return exists
}

// Execute runs the plugin for the given method
func (m *PluginManager) Execute(ctx context.Context, method string, id jsonrpc.ID, params json.RawMessage, caller UpstreamCaller) *jsonrpc.Response {
	m.mu.RLock()
	plugin, exists := m.plugins[method]
	m.mu.RUnlock()

	if !exists {
		return jsonrpc.NewErrorResponse(id, jsonrpc.NewError(ErrCodePluginNotFound, "plugin not found"))
	}

	// Create execution context with timeout
	execCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	// Execute plugin in goroutine to handle timeout
	resultCh := make(chan *jsonrpc.Response, 1)
	go func() {
		resultCh <- m.executePlugin(plugin, id, params, caller)
	}()

	select {
	case <-execCtx.Done():
		if execCtx.Err() == context.DeadlineExceeded {
			m.logger.Warn().
				Str("method", method).
				Dur("timeout", m.timeout).
				Msg("plugin execution timed out")
			return jsonrpc.NewErrorResponse(id, jsonrpc.NewError(ErrCodePluginTimeout, "plugin execution timed out"))
		}
		return jsonrpc.NewErrorResponse(id, jsonrpc.NewError(jsonrpc.CodeInternalError, "request cancelled"))
	case result := <-resultCh:
		return result
	}
}

// executePlugin runs the plugin with the given parameters
func (m *PluginManager) executePlugin(plugin *Plugin, id jsonrpc.ID, params json.RawMessage, caller UpstreamCaller) *jsonrpc.Response {
	// Create new runtime for this execution (thread safety)
	runtime := NewRuntime(m.logger)

	// Set up upstream caller
	runtime.SetupUpstreamCaller(caller)

	// Load the plugin script
	_, err := runtime.RunScript(plugin.Script)
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("plugin", plugin.Name).
			Msg("failed to load plugin script")
		return jsonrpc.NewErrorResponse(id, jsonrpc.NewError(ErrCodePluginExecution, fmt.Sprintf("script error: %v", err)))
	}

	// Parse params
	var parsedParams interface{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &parsedParams); err != nil {
			return jsonrpc.NewErrorResponse(id, jsonrpc.NewError(ErrCodePluginInvalidArgs, fmt.Sprintf("invalid params: %v", err)))
		}
	}

	// Call execute function
	result, err := m.callExecute(runtime, parsedParams)
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("plugin", plugin.Name).
			Msg("plugin execution failed")
		return jsonrpc.NewErrorResponse(id, jsonrpc.NewError(ErrCodePluginExecution, err.Error()))
	}

	// Marshal result
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return jsonrpc.NewErrorResponse(id, jsonrpc.NewError(jsonrpc.CodeInternalError, fmt.Sprintf("failed to marshal result: %v", err)))
	}

	return &jsonrpc.Response{
		JSONRPC: jsonrpc.Version,
		ID:      id,
		Result:  resultJSON,
	}
}

// callExecute calls the execute function in the plugin
func (m *PluginManager) callExecute(runtime *Runtime, params interface{}) (interface{}, error) {
	vm := runtime.VM()

	// Get execute function
	executeVal := vm.Get("execute")
	if executeVal == nil || goja.IsUndefined(executeVal) {
		return nil, fmt.Errorf("execute function not defined")
	}

	execute, ok := goja.AssertFunction(executeVal)
	if !ok {
		return nil, fmt.Errorf("execute is not a function")
	}

	// Get upstream object (already set up)
	upstreamVal := vm.Get("upstream")

	// Call execute(params, upstream)
	result, err := execute(goja.Undefined(), vm.ToValue(params), upstreamVal)
	if err != nil {
		// Check if it's a JS exception
		if jsErr, ok := err.(*goja.Exception); ok {
			return nil, fmt.Errorf("%s", jsErr.String())
		}
		return nil, err
	}

	return result.Export(), nil
}

// GetMethods returns all registered plugin methods
func (m *PluginManager) GetMethods() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	methods := make([]string, 0, len(m.plugins))
	for method := range m.plugins {
		methods = append(methods, method)
	}
	return methods
}

// Close releases all resources
func (m *PluginManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins = make(map[string]*Plugin)
	m.logger.Info().Msg("plugin manager closed")
}
