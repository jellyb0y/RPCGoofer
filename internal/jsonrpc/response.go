package jsonrpc

import (
	"bytes"
	"encoding/json"
)

// Response represents a JSON-RPC response
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	ID      ID              `json:"id"`
}

// HasError returns true if the response contains an error
func (r *Response) HasError() bool {
	return r.Error != nil
}

// IsSuccess returns true if the response is successful
func (r *Response) IsSuccess() bool {
	return r.Error == nil
}

// ResultIsNull returns true if the response result is JSON null
func (r *Response) ResultIsNull() bool {
	if r == nil {
		return true
	}
	if r.Result == nil || len(r.Result) == 0 {
		return true
	}
	return bytes.Equal(r.Result, []byte("null"))
}

// NewResponse creates a successful response
func NewResponse(id ID, result interface{}) (*Response, error) {
	resp := &Response{
		JSONRPC: Version,
		ID:      id,
	}

	if result != nil {
		resultBytes, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		resp.Result = resultBytes
	}

	return resp, nil
}

// NewResponseRaw creates a response with raw JSON result
func NewResponseRaw(id ID, result json.RawMessage) *Response {
	return &Response{
		JSONRPC: Version,
		Result:  result,
		ID:      id,
	}
}

// NewErrorResponse creates an error response
func NewErrorResponse(id ID, err *Error) *Response {
	return &Response{
		JSONRPC: Version,
		Error:   err,
		ID:      id,
	}
}

// ParseResponse parses a JSON-RPC response from bytes
func ParseResponse(data []byte) (*Response, error) {
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ParseBatchResponse parses a batch of JSON-RPC responses
func ParseBatchResponse(data []byte) ([]*Response, bool, error) {
	data = trimWhitespace(data)
	if len(data) == 0 {
		return nil, false, ErrInvalidRequest
	}

	if data[0] == '[' {
		var responses []*Response
		if err := json.Unmarshal(data, &responses); err != nil {
			return nil, true, err
		}
		return responses, true, nil
	}

	resp, err := ParseResponse(data)
	if err != nil {
		return nil, false, err
	}
	return []*Response{resp}, false, nil
}

// Bytes returns the response as JSON bytes
func (r *Response) Bytes() ([]byte, error) {
	return json.Marshal(r)
}

// Clone creates a copy of the response
func (r *Response) Clone() *Response {
	clone := &Response{
		JSONRPC: r.JSONRPC,
		ID:      r.ID,
	}
	if r.Result != nil {
		clone.Result = make(json.RawMessage, len(r.Result))
		copy(clone.Result, r.Result)
	}
	if r.Error != nil {
		clone.Error = &Error{
			Code:    r.Error.Code,
			Message: r.Error.Message,
		}
		if r.Error.Data != nil {
			clone.Error.Data = make(json.RawMessage, len(r.Error.Data))
			copy(clone.Error.Data, r.Error.Data)
		}
	}
	return clone
}

// IsRetryableError checks if the error is retryable
// By default, ALL errors are retryable except for client errors
// (invalid request, method not found, invalid params)
func (r *Response) IsRetryableError() bool {
	if r.Error == nil {
		return false
	}

	// Client errors that should NOT be retried
	// These indicate problems with the request itself, not the server
	// Note: MethodNotFound (-32601) is retryable - different upstreams may support different methods (e.g. debug/trace)
	switch r.Error.Code {
	case CodeParseError:     // -32700: Parse error
		return false
	case CodeInvalidRequest: // -32600: Invalid Request
		return false
	case CodeInvalidParams:  // -32602: Invalid params
		return false
	}

	// Check for non-retryable error messages (contract/execution errors)
	// These are logical errors in the request, not server issues
	switch {
	case contains(r.Error.Message, "execution reverted"):
		return false
	case contains(r.Error.Message, "insufficient funds"):
		return false
	case contains(r.Error.Message, "nonce too low"):
		return false
	case contains(r.Error.Message, "nonce too high"):
		return false
	case contains(r.Error.Message, "already known"):
		return false
	case contains(r.Error.Message, "replacement transaction underpriced"):
		return false
	}

	// All other errors are retryable (server errors, internal errors, etc.)
	return true
}

// contains checks if s contains substr (case insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsLower(toLower(s), toLower(substr))
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// MarshalBatchResponse marshals multiple responses as a JSON array
func MarshalBatchResponse(responses []*Response) ([]byte, error) {
	return json.Marshal(responses)
}

// GetResultAs unmarshals the result into the provided type
func (r *Response) GetResultAs(v interface{}) error {
	if r.Result == nil {
		return nil
	}
	return json.Unmarshal(r.Result, v)
}
