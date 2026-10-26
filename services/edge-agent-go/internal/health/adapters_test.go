package health_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/health"
)

type telemetryCall struct {
	err         error
	unsupported bool
}

func (s telemetryCall) Read(ctx context.Context) ([]domain.Reading, error) {
	if s.unsupported {
		adapter.ReportUnsupportedField(ctx)
	}
	return []domain.Reading{{DeviceID: "battery-01"}}, s.err
}

type commandCall struct {
	err  error
	send bool
}

func (s commandCall) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }
func (s commandCall) Execute(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, error) {
	if s.send {
		if err := adapter.BeforeSend(ctx); err != nil {
			return adapter.CommandResult{}, err
		}
	}
	return adapter.CommandResult{CommandID: cmd.ID}, s.err
}

type queryCommand struct{ commandCall }

func (queryCommand) Outcome(context.Context, adapter.Command) (adapter.CommandResult, bool, error) {
	return adapter.CommandResult{}, true, nil
}

type unavailableQueue struct{}

func (unavailableQueue) Stats(context.Context) (domain.QueueStats, error) {
	return domain.QueueStats{}, errors.New("disk unavailable")
}

func TestAdapterMetricsRemainAvailableWhenQueueFails(t *testing.T) {
	m := health.NewAdapterMetrics()
	ctx := context.Background()
	for _, call := range []telemetryCall{{unsupported: true}, {err: context.Canceled}, {err: adapter.ErrTimeout}, {err: fmt.Errorf("wire: %w", adapter.ErrProtocolTimeout)}, {err: adapter.ErrUnsupported}} {
		_, _ = m.Telemetry("modbus", "battery-01", call).Read(ctx)
	}
	for _, call := range []commandCall{{send: true}, {err: adapter.ErrUnsupported}, {err: adapter.ErrTimeout}, {err: adapter.ErrProtocolTimeout}} {
		_, _ = m.Command("mqtt", "meter-01", call).Execute(ctx, adapter.Command{ID: "cmd-1", DeviceID: "meter-01"})
	}
	handler := health.NewHandler(unavailableQueue{}, domain.FallbackRateLimitPolicy{}, m)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, want := range []string{
		"ambagrid_edge_queue_stats_available 0",
		`ambagrid_edge_adapter_read_errors_total{adapter="modbus",device_id="battery-01"} 4`,
		`ambagrid_edge_adapter_command_errors_total{adapter="mqtt",device_id="meter-01"} 3`,
		`ambagrid_edge_adapter_protocol_timeouts_total{adapter="modbus",device_id="battery-01",operation="read"} 1`,
		`ambagrid_edge_adapter_protocol_timeouts_total{adapter="mqtt",device_id="meter-01",operation="command"} 1`,
		`ambagrid_edge_adapter_unsupported_total{adapter="modbus",device_id="battery-01",operation="read"} 2`,
		`ambagrid_edge_adapter_unsupported_total{adapter="mqtt",device_id="meter-01",operation="command"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	for _, name := range []string{"read", "command"} {
		prefix := "ambagrid_edge_adapter_last_successful_" + name + "_timestamp_seconds"
		if !strings.Contains(body, prefix+"{") || strings.Contains(body, prefix+`{adapter="modbus",device_id="battery-01"} 0`) || strings.Contains(body, prefix+`{adapter="mqtt",device_id="meter-01"} 0`) {
			t.Errorf("missing successful %s timestamp: %s", name, body)
		}
	}
}

func TestCommandMetricsPreserveOptionalReconciliationAndIgnoreCachedSuccess(t *testing.T) {
	m := health.NewAdapterMetrics()
	bare := m.Command("plain", "meter-01", commandCall{})
	if _, ok := bare.(adapter.Reconciler); ok {
		t.Fatal("wrapper invented reconciliation")
	}
	queried := m.Command("query", "meter-02", queryCommand{})
	r, ok := queried.(adapter.Reconciler)
	if !ok {
		t.Fatal("wrapper removed reconciliation")
	}
	if _, applied, err := r.Outcome(context.Background(), adapter.Command{}); err != nil || !applied {
		t.Fatalf("reconcile: %t %v", applied, err)
	}
	_, _ = bare.Execute(context.Background(), adapter.Command{}) // no send hook: a cached success
	response := httptest.NewRecorder()
	health.NewHandler(unavailableQueue{}, domain.FallbackRateLimitPolicy{}, m).ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(response.Body.String(), `ambagrid_edge_adapter_last_successful_command_timestamp_seconds{adapter="plain",device_id="meter-01"} 0`) {
		t.Fatal("cached command refreshed device health")
	}
}
