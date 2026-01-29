package jsonrpc

import "encoding/json"

// Version is the JSON-RPC version
const Version = "2.0"

// Standard JSON-RPC error codes
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// Server error codes range: -32000 to -32099
	CodeServerError = -32000
)

// ID represents a JSON-RPC request/response ID
// It can be a string, number, or null
type ID struct {
	value interface{}
}

// NewIDString creates an ID from a string
func NewIDString(s string) ID {
	return ID{value: s}
}

// NewIDInt creates an ID from an integer
func NewIDInt(n int64) ID {
	return ID{value: n}
}

// NewIDNull creates a null ID
func NewIDNull() ID {
	return ID{value: nil}
}

// IsNull returns true if the ID is null
func (id ID) IsNull() bool {
	return id.value == nil
}

// Value returns the underlying value
func (id ID) Value() interface{} {
	return id.value
}

// MarshalJSON implements json.Marshaler
func (id ID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.value)
}

// UnmarshalJSON implements json.Unmarshaler
func (id *ID) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &id.value)
}

// Error represents a JSON-RPC error
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface
func (e *Error) Error() string {
	return e.Message
}

// NewError creates a new JSON-RPC error
func NewError(code int, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
	}
}

// NewErrorWithData creates a new JSON-RPC error with data
func NewErrorWithData(code int, message string, data interface{}) *Error {
	e := &Error{
		Code:    code,
		Message: message,
	}
	if data != nil {
		if rawData, err := json.Marshal(data); err == nil {
			e.Data = rawData
		}
	}
	return e
}

// Common errors
var (
	ErrParse          = NewError(CodeParseError, "Parse error")
	ErrInvalidRequest = NewError(CodeInvalidRequest, "Invalid Request")
	ErrMethodNotFound = NewError(CodeMethodNotFound, "Method not found")
	ErrInvalidParams  = NewError(CodeInvalidParams, "Invalid params")
	ErrInternal       = NewError(CodeInternalError, "Internal error")
)

// SubscriptionNotification represents a subscription event notification
type SubscriptionNotification struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  SubscriptionParams     `json:"params"`
}

// SubscriptionParams contains the subscription notification parameters
type SubscriptionParams struct {
	Subscription string          `json:"subscription"`
	Result       json.RawMessage `json:"result"`
}

// BlockHeader represents an Ethereum block header (for newHeads)
type BlockHeader struct {
	Hash             string `json:"hash"`
	ParentHash       string `json:"parentHash"`
	Number           string `json:"number"`
	Timestamp        string `json:"timestamp"`
	Nonce            string `json:"nonce,omitempty"`
	Difficulty       string `json:"difficulty,omitempty"`
	GasLimit         string `json:"gasLimit"`
	GasUsed          string `json:"gasUsed"`
	Miner            string `json:"miner"`
	ExtraData        string `json:"extraData"`
	LogsBloom        string `json:"logsBloom"`
	TransactionsRoot string `json:"transactionsRoot"`
	StateRoot        string `json:"stateRoot"`
	ReceiptsRoot     string `json:"receiptsRoot"`
	BaseFeePerGas    string `json:"baseFeePerGas,omitempty"`
}

// Log represents an Ethereum log entry
type Log struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	BlockHash        string   `json:"blockHash"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}
