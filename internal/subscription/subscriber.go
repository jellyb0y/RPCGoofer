package subscription

import (
	"encoding/json"
)

// SubscriptionEvent represents an event from an upstream subscription
type SubscriptionEvent struct {
	UpstreamName string
	SubType      SubscriptionType
	Result       json.RawMessage
}

// Subscriber is the interface for subscription event consumers
type Subscriber interface {
	// OnEvent is called when a new event is received from an upstream
	OnEvent(event SubscriptionEvent)
	// ID returns a unique identifier for this subscriber
	ID() string
}
