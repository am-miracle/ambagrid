package mqtt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
)

type subscriptionFunc func(context.Context, *paho.Subscribe) (*paho.Suback, error)

func (f subscriptionFunc) Subscribe(ctx context.Context, sub *paho.Subscribe) (*paho.Suback, error) {
	return f(ctx, sub)
}

func TestSubscriptionRestoreRetriesBeforeReportingReady(t *testing.T) {
	transport := &AutopahoTransport{generation: 1, retryDelay: time.Millisecond}
	attempts := 0
	client := subscriptionFunc(func(context.Context, *paho.Subscribe) (*paho.Suback, error) {
		attempts++
		if transport.Connected() {
			t.Error("ready before subscriptions restored")
		}
		if attempts == 1 {
			return nil, errors.New("transient subscription timeout")
		}
		return &paho.Suback{Reasons: []byte{1}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	transport.restoreSubscriptions(ctx, client, []string{"meter/+/reply"}, 1)
	if attempts < 2 || !transport.Connected() {
		t.Fatalf("attempts=%d ready=%t, want retry and readiness", attempts, transport.Connected())
	}
}

func TestStaleSubscriptionRestoreCannotReportReady(t *testing.T) {
	transport := &AutopahoTransport{generation: 1, retryDelay: time.Millisecond}
	client := subscriptionFunc(func(context.Context, *paho.Subscribe) (*paho.Suback, error) {
		transport.connectionDown()
		return &paho.Suback{Reasons: []byte{1}}, nil
	})
	transport.restoreSubscriptions(context.Background(), client, []string{"meter/+/reply"}, 1)
	if transport.Connected() {
		t.Fatal("disconnected generation reported ready")
	}
}
