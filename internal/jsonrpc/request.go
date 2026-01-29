package jsonrpc

import (
	"encoding/json"
	"fmt"
)

// Request represents a JSON-RPC request
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      ID              `json:"id"`
}

// Validate checks if the request is valid
func (r *Request) Validate() error {
	if r.JSONRPC != Version {
		return fmt.Errorf("invalid jsonrpc version: %s", r.JSONRPC)
	}
	if r.Method == "" {
		return fmt.Errorf("method is required")
	}
	return nil
}

// IsNotification returns true if this is a notification (no ID)
func (r *Request) IsNotification() bool {
	return r.ID.IsNull()
}

// Clone creates a copy of the request
func (r *Request) Clone() *Request {
	clone := &Request{
		JSONRPC: r.JSONRPC,
		Method:  r.Method,
		ID:      r.ID,
	}
	if r.Params != nil {
		clone.Params = make(json.RawMessage, len(r.Params))
		copy(clone.Params, r.Params)
	}
	return clone
}

// ParseRequest parses a single JSON-RPC request from bytes
func ParseRequest(data []byte) (*Request, error) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}
	return &req, nil
}

// ParseBatchRequest parses a batch of JSON-RPC requests
// Returns a slice of requests, or a single request if not a batch
func ParseBatchRequest(data []byte) ([]*Request, bool, error) {
	// Check if it's an array (batch) or object (single)
	data = trimWhitespace(data)
	if len(data) == 0 {
		return nil, false, ErrInvalidRequest
	}

	if data[0] == '[' {
		// Batch request
		var requests []*Request
		if err := json.Unmarshal(data, &requests); err != nil {
			return nil, true, fmt.Errorf("failed to parse batch request: %w", err)
		}
		if len(requests) == 0 {
			return nil, true, ErrInvalidRequest
		}
		return requests, true, nil
	}

	// Single request
	req, err := ParseRequest(data)
	if err != nil {
		return nil, false, err
	}
	return []*Request{req}, false, nil
}

// NewRequest creates a new JSON-RPC request
func NewRequest(method string, params interface{}, id ID) (*Request, error) {
	req := &Request{
		JSONRPC: Version,
		Method:  method,
		ID:      id,
	}

	if params != nil {
		paramsBytes, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params: %w", err)
		}
		req.Params = paramsBytes
	}

	return req, nil
}

// MarshalJSON implements custom JSON marshaling
func (r *Request) MarshalJSON() ([]byte, error) {
	type requestAlias Request
	return json.Marshal((*requestAlias)(r))
}

// Bytes returns the request as JSON bytes
func (r *Request) Bytes() ([]byte, error) {
	return json.Marshal(r)
}

// trimWhitespace removes leading whitespace from byte slice
func trimWhitespace(data []byte) []byte {
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return data[i:]
		}
	}
	return data
}

// IsSubscribeMethod returns true if the method is eth_subscribe
func (r *Request) IsSubscribeMethod() bool {
	return r.Method == "eth_subscribe"
}

// IsUnsubscribeMethod returns true if the method is eth_unsubscribe
func (r *Request) IsUnsubscribeMethod() bool {
	return r.Method == "eth_unsubscribe"
}

// GetSubscriptionType extracts the subscription type from params
// Returns the subscription type (e.g., "newHeads", "logs") and any additional params
func (r *Request) GetSubscriptionType() (string, json.RawMessage, error) {
	if !r.IsSubscribeMethod() {
		return "", nil, fmt.Errorf("not a subscribe request")
	}

	var params []json.RawMessage
	if err := json.Unmarshal(r.Params, &params); err != nil {
		return "", nil, fmt.Errorf("invalid params format: %w", err)
	}

	if len(params) == 0 {
		return "", nil, fmt.Errorf("subscription type is required")
	}

	var subType string
	if err := json.Unmarshal(params[0], &subType); err != nil {
		return "", nil, fmt.Errorf("invalid subscription type: %w", err)
	}

	var additionalParams json.RawMessage
	if len(params) > 1 {
		additionalParams = params[1]
	}

	return subType, additionalParams, nil
}

// GetUnsubscribeID extracts the subscription ID from unsubscribe params
func (r *Request) GetUnsubscribeID() (string, error) {
	if !r.IsUnsubscribeMethod() {
		return "", fmt.Errorf("not an unsubscribe request")
	}

	var params []string
	if err := json.Unmarshal(r.Params, &params); err != nil {
		return "", fmt.Errorf("invalid params format: %w", err)
	}

	if len(params) == 0 {
		return "", fmt.Errorf("subscription ID is required")
	}

	return params[0], nil
}
