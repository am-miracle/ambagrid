package health

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

func (m *AdapterMetrics) initialize(name, device, operation string) {
	m.timeouts.WithLabelValues(name, device, operation)
	m.unsupported.WithLabelValues(name, device, operation)
	if operation == "read" {
		m.lastRead.WithLabelValues(name, device)
		m.readErrors.WithLabelValues(name, device)
	} else {
		m.lastCommand.WithLabelValues(name, device)
		m.commandErrors.WithLabelValues(name, device)
	}
}

func (m *AdapterMetrics) failure(name, device, operation string, err error) {
	if operation == "read" {
		m.readErrors.WithLabelValues(name, device).Inc()
	} else {
		m.commandErrors.WithLabelValues(name, device).Inc()
	}
	if errors.Is(err, adapter.ErrProtocolTimeout) {
		m.timeouts.WithLabelValues(name, device, operation).Inc()
	}
}

type observedTelemetry struct {
	metrics      *AdapterMetrics
	name, device string
	inner        adapter.TelemetryAdapter
}

func (m *AdapterMetrics) Telemetry(name, device string, inner adapter.TelemetryAdapter) adapter.TelemetryAdapter {
	m.initialize(name, device, "read")
	return observedTelemetry{m, name, device, inner}
}

func (a observedTelemetry) Read(ctx context.Context) ([]domain.Reading, error) {
	var reported atomic.Bool
	ctx = adapter.WithUnsupportedFieldHook(ctx, func() {
		reported.Store(true)
		a.metrics.unsupported.WithLabelValues(a.name, a.device, "read").Inc()
	})
	readings, err := a.inner.Read(ctx)
	if err != nil {
		a.metrics.failure(a.name, a.device, "read", err)
		if errors.Is(err, adapter.ErrUnsupported) && !reported.Load() {
			a.metrics.unsupported.WithLabelValues(a.name, a.device, "read").Inc()
		}
	} else if len(readings) > 0 {
		a.metrics.lastRead.WithLabelValues(a.name, a.device).SetToCurrentTime()
	}
	return readings, err
}

type observedCommand struct {
	adapter.CommandAdapter
	metrics      *AdapterMetrics
	name, device string
}

type observedReconciler struct {
	*observedCommand
	reconciler adapter.Reconciler
}

// Command preserves optional reconciliation. When wrapping a ledger, cached
// successes do not refresh hardware health because they never call BeforeSend.
func (m *AdapterMetrics) Command(name, device string, inner adapter.CommandAdapter) adapter.CommandAdapter {
	m.initialize(name, device, "command")
	observed := &observedCommand{inner, m, name, device}
	if r, ok := inner.(adapter.Reconciler); ok {
		return observedReconciler{observed, r}
	}
	return observed
}

func (a *observedCommand) Execute(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, error) {
	var sent atomic.Bool
	ctx = adapter.WithSendHook(ctx, func() error { sent.Store(true); return nil })
	result, err := a.CommandAdapter.Execute(ctx, cmd)
	if err != nil {
		a.metrics.failure(a.name, a.device, "command", err)
		if errors.Is(err, adapter.ErrUnsupported) {
			a.metrics.unsupported.WithLabelValues(a.name, a.device, "command").Inc()
		}
	} else if sent.Load() {
		a.metrics.lastCommand.WithLabelValues(a.name, a.device).Set(float64(time.Now().UnixNano()) / 1e9)
	}
	return result, err
}

func (a observedReconciler) Outcome(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, bool, error) {
	// An outcome query is not another application of the original command.
	return a.reconciler.Outcome(ctx, cmd)
}
