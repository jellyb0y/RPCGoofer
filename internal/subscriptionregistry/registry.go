package subscriptionregistry

import (
	"context"
	"encoding/json"
)

// SubscriptionType represents the type of subscription (newHeads, logs, etc.)
type SubscriptionType string

const (
	SubTypeNewHeads               SubscriptionType = "newHeads"
	SubTypeLogs                   SubscriptionType = "logs"
	SubTypeNewPendingTransactions SubscriptionType = "newPendingTransactions"
	SubTypeSyncing                SubscriptionType = "syncing"
)

// SubscriptionTarget is the interface an upstream implements for the registry to drive subscribe/unsubscribe.
type SubscriptionTarget interface {
	Subscribe(ctx context.Context, subType SubscriptionType, params json.RawMessage) (subID string, err error)
	Unsubscribe(subID string)
}

// Registry is the interface for the subscription registry (implemented by subscription.Registry).
// Upstream package uses this to avoid import cycle with subscription.
type Registry interface {
	Register(upstreamName string, target SubscriptionTarget)
	Unregister(upstreamName string)
	DeliverEvent(upstreamName string, subType SubscriptionType, params json.RawMessage, result json.RawMessage)
}
