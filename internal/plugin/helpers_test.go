package plugin

import (
	"encoding/json"
	"os"

	"github.com/rs/zerolog"
)

// testLogger creates a zerolog logger for tests that writes to os.Stderr
func testLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).Level(zerolog.Disabled)
}

// mockUpstreamCaller implements UpstreamCaller for testing
type mockUpstreamCaller struct {
	callFn      func(method string, params interface{}) (json.RawMessage, error)
	batchCallFn func(calls []CallRequest) ([]json.RawMessage, error)
}

func (m *mockUpstreamCaller) Call(method string, params interface{}) (json.RawMessage, error) {
	if m.callFn != nil {
		return m.callFn(method, params)
	}
	return json.RawMessage(`null`), nil
}

func (m *mockUpstreamCaller) BatchCall(calls []CallRequest) ([]json.RawMessage, error) {
	if m.batchCallFn != nil {
		return m.batchCallFn(calls)
	}
	return nil, nil
}

// writeTestPlugin writes a JS plugin to a temp directory and returns the dir path.
// Caller is responsible for cleanup via t.Cleanup or defer os.RemoveAll.
func writeTestPlugin(dir, filename, content string) error {
	return os.WriteFile(dir+"/"+filename, []byte(content), 0644)
}
