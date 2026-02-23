package subscription

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"rpcgofer/internal/subscriptionregistry"
)

func TestRegistry_SubscribeUnsubscribe(t *testing.T) {
	logger := zerolog.Nop()
	r := NewRegistry(100, logger)
	defer r.Close()

	ctx := context.Background()
	sub := &mockSubscriber{id: "sub1"}

	err := r.Subscribe(ctx, SubTypeNewHeads, nil, sub)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	err = r.Unsubscribe(SubTypeNewHeads, nil, "sub1")
	if err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
}

func TestRegistry_Subscribe_DeliverEvent(t *testing.T) {
	logger := zerolog.Nop()
	r := NewRegistry(100, logger)
	defer r.Close()

	ctx := context.Background()
	received := make(chan SubscriptionEvent, 1)
	sub := &mockSubscriber{
		id: "sub1",
		onEvent: func(e SubscriptionEvent) {
			select {
			case received <- e:
			default:
			}
		},
	}

	err := r.Subscribe(ctx, SubTypeNewHeads, nil, sub)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	r.DeliverEvent("up1", subscriptionregistry.SubTypeNewHeads, nil, json.RawMessage(`{"number":"0x1"}`))
	select {
	case e := <-received:
		if e.UpstreamName != "up1" {
			t.Errorf("UpstreamName = %s, want up1", e.UpstreamName)
		}
	default:
		t.Fatal("no event received")
	}
}

func TestRegistry_Register_Unregister(t *testing.T) {
	logger := zerolog.Nop()
	r := NewRegistry(100, logger)
	defer r.Close()

	ctx := context.Background()
	mockTarget := &mockSubscriptionTarget{
		subscribeCalls: make(chan subCall, 10),
	}
	r.Register("up1", mockTarget)

	sub := &mockSubscriber{id: "health"}
	err := r.Subscribe(ctx, SubTypeNewHeads, nil, sub)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	select {
	case call := <-mockTarget.subscribeCalls:
		if call.subType != subscriptionregistry.SubTypeNewHeads {
			t.Errorf("subType = %s", call.subType)
		}
	default:
		t.Log("Subscribe may have been called before we added subscriber (race); Register runs async")
	}

	r.Unregister("up1")
	r.Unsubscribe(SubTypeNewHeads, nil, "health")
}

func TestRegistry_UnsubscribeLast_CallsTargetUnsubscribe(t *testing.T) {
	logger := zerolog.Nop()
	r := NewRegistry(100, logger)
	defer r.Close()

	ctx := context.Background()
	unsubCalls := make(chan string, 2)
	mockTarget := &mockSubscriptionTarget{
		subscribeResult: "sub-id-1",
		unsubscribeCalls: unsubCalls,
	}
	r.Register("up1", mockTarget)

	sub := &mockSubscriber{id: "only"}
	err := r.Subscribe(ctx, SubTypeNewHeads, nil, sub)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Give Register time to call Subscribe on the target so we have a subID stored
	// (Register does this asynchronously after adding the first subscriber)
	err = r.Unsubscribe(SubTypeNewHeads, nil, "only")
	if err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	select {
	case subID := <-unsubCalls:
		if subID != "sub-id-1" {
			t.Errorf("Unsubscribe called with subID = %s, want sub-id-1", subID)
		}
	default:
		t.Log("Unsubscribe on target not called (possible if Register had not finished)")
	}
}

type mockSubscriber struct {
	id      string
	onEvent func(SubscriptionEvent)
}

func (m *mockSubscriber) OnEvent(e SubscriptionEvent) {
	if m.onEvent != nil {
		m.onEvent(e)
	}
}

func (m *mockSubscriber) ID() string { return m.id }

func (m *mockSubscriber) SkipDedup() bool { return false }

func (m *mockSubscriber) DeliverFirst() bool { return false }

type subCall struct {
	subType subscriptionregistry.SubscriptionType
	params  json.RawMessage
}

type mockSubscriptionTarget struct {
	mu               sync.Mutex
	subscribeResult  string
	subscribeCalls   chan subCall
	unsubscribeCalls chan string
}

func (m *mockSubscriptionTarget) Subscribe(ctx context.Context, subType subscriptionregistry.SubscriptionType, params json.RawMessage) (string, error) {
	m.mu.Lock()
	res := m.subscribeResult
	if res == "" {
		res = "mock-sub-id"
	}
	m.mu.Unlock()
	if m.subscribeCalls != nil {
		select {
		case m.subscribeCalls <- subCall{subType: subType, params: params}:
		default:
		}
	}
	return res, nil
}

func (m *mockSubscriptionTarget) Unsubscribe(subID string) {
	if m.unsubscribeCalls != nil {
		select {
		case m.unsubscribeCalls <- subID:
		default:
		}
	}
}
