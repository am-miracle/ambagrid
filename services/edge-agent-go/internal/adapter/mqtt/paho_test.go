package mqtt

import (
	"context"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	paho "github.com/eclipse/paho.mqtt.golang"
)

type doneToken struct{}

func (doneToken) Wait() bool                     { return true }
func (doneToken) WaitTimeout(time.Duration) bool { return true }
func (doneToken) Done() <-chan struct{}          { done := make(chan struct{}); close(done); return done }
func (doneToken) Error() error                   { return nil }

type disconnectedClient struct{ paho.Client }

func (disconnectedClient) IsConnectionOpen() bool                                 { return false }
func (disconnectedClient) Subscribe(string, byte, paho.MessageHandler) paho.Token { return doneToken{} }
func TestDisconnectedBrokerDoesNotMarkCommandSent(t *testing.T) {
	transport := &PahoTransport{client: disconnectedClient{}, subscriptions: make(map[string]paho.MessageHandler)}
	a, err := New(context.Background(), Config{TopicPrefix: "ambagrid/site-01", Capabilities: adapter.Capabilities{Capabilities: adapter.CapabilitySet{RelayControl: true}}}, transport)
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	ctx := adapter.WithSendHook(context.Background(), func() error { marked++; return nil })
	_, err = a.Execute(ctx, adapter.Command{ID: "cmd-1", Type: adapter.CommandDisconnect, DeviceID: "meter-01"})
	if err == nil {
		t.Fatal("accepted disconnected broker")
	}
	if marked != 0 {
		t.Fatalf("unsent command marked sent %d times", marked)
	}
}
