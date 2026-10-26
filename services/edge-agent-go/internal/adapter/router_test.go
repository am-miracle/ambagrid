package adapter_test

import (
	"context"
	"errors"
	"testing"

	"edge-agent-go/internal/adapter"
)

type deviceCommand struct {
	id      string
	calls   int
	allowed bool
}

func (d *deviceCommand) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }
func (d *deviceCommand) Execute(_ context.Context, cmd adapter.Command) (adapter.CommandResult, error) {
	d.calls++
	if cmd.DeviceID != d.id {
		panic("routed to wrong device")
	}
	if !d.allowed {
		return adapter.CommandResult{}, adapter.ErrUnsupported
	}
	return adapter.CommandResult{CommandID: cmd.ID}, nil
}
func TestRouterSelectsExactDeviceWithoutFallback(t *testing.T) {
	a, b := &deviceCommand{id: "a", allowed: true}, &deviceCommand{id: "b"}
	routes := map[string]adapter.CommandAdapter{"a": a, "b": b}
	router := adapter.NewRouter(routes)
	delete(routes, "a")
	if _, err := router.Execute(context.Background(), adapter.Command{ID: "cmd-a", DeviceID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Execute(context.Background(), adapter.Command{ID: "cmd-b", DeviceID: "b"}); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := router.Execute(context.Background(), adapter.Command{ID: "unknown", DeviceID: "unknown"}); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatal(err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls a=%d b=%d", a.calls, b.calls)
	}
}
