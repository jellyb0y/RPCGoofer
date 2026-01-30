package subscription

import (
	"encoding/json"
)

// SendFunc is a callback function for sending data to the client
type SendFunc func(data []byte)

// SubscriptionType represents the type of subscription
type SubscriptionType string

const (
	SubTypeNewHeads               SubscriptionType = "newHeads"
	SubTypeLogs                   SubscriptionType = "logs"
	SubTypeNewPendingTransactions SubscriptionType = "newPendingTransactions"
	SubTypeSyncing                SubscriptionType = "syncing"
)

// SubscriptionNotification represents a subscription notification
type SubscriptionNotification struct {
	JSONRPC string             `json:"jsonrpc"`
	Method  string             `json:"method"`
	Params  NotificationParams `json:"params"`
}

// NotificationParams contains the notification parameters
type NotificationParams struct {
	Subscription string          `json:"subscription"`
	Result       json.RawMessage `json:"result"`
}

// NewNotification creates a new subscription notification
func NewNotification(subID string, result json.RawMessage) *SubscriptionNotification {
	return &SubscriptionNotification{
		JSONRPC: "2.0",
		Method:  "eth_subscription",
		Params: NotificationParams{
			Subscription: subID,
			Result:       result,
		},
	}
}

// Bytes returns the notification as JSON bytes
func (n *SubscriptionNotification) Bytes() ([]byte, error) {
	return json.Marshal(n)
}
