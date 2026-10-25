// mqtt carries meter commands to devices over an MQTT broker using the
// AmbaGrid command protocol described in docs/hardware-adapters.md.
package mqtt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"edge-agent-go/internal/adapter"
)

// Transport is the broker connection. Publish calls beforeSend, when non-nil,
// after readiness checks and immediately before handing off a command. An error
// from that hook forbids publication. Replies and queries pass nil.
// Deliver callbacks must return quickly;
// the adapter only decodes and hands off on them.
type Transport interface {
	Publish(ctx context.Context, topic string, payload []byte, beforeSend func() error) error
	Subscribe(ctx context.Context, filter string, deliver func(topic string, payload []byte)) error
}

type Config struct {
	// TopicPrefix scopes this gateway's meters, e.g. "ambagrid/ng-kaji-01".
	TopicPrefix  string
	Capabilities adapter.Capabilities
}

// request is published to {prefix}/{device}/command.
type request struct {
	Ref    string `json:"ref"`
	Op     string `json:"op"`
	Device string `json:"device"`
	Wh     int64  `json:"wh,omitempty"`
	Token  string `json:"token,omitempty"`
	Closed *bool  `json:"closed,omitempty"`
	// Target is the command reference an "outcome" query asks about.
	Target string `json:"target,omitempty"`
}

// reply arrives on {prefix}/{device}/command/reply.
type reply struct {
	Ref       string `json:"ref"`
	OK        bool   `json:"ok"`
	TsMs      int64  `json:"ts_ms,omitempty"`
	Relay     *int   `json:"relay,omitempty"`
	BalanceWh *int64 `json:"balance_wh,omitempty"`
	Tamper    *int   `json:"tamper,omitempty"`
	Applied   *bool  `json:"applied,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"msg,omitempty"`
}

type waiter struct {
	replyTopic string
	replies    chan reply
}

type Adapter struct {
	cfg       Config
	transport Transport
	now       func() time.Time

	mu      sync.Mutex
	waiting map[string]waiter
}

// reconcilingAdapter is returned for devices declaring outcome_query, so the
// ledger only treats a device as able to answer when it really can.
type reconcilingAdapter struct {
	*Adapter
}

// New subscribes to device replies and returns an adapter.CommandAdapter,
// which also implements adapter.Reconciler when the declaration has outcome_query.
func New(ctx context.Context, cfg Config, transport Transport) (adapter.CommandAdapter, error) {
	// QoS 1 can redeliver below the platform ledger. Monetary credits need
	// receiver-side reference deduplication even for a single publish attempt.
	if cfg.Capabilities.Capabilities.PrepaidCredit && !cfg.Capabilities.Capabilities.IdempotentRefs {
		return nil, fmt.Errorf("%w: MQTT prepaid credit requires idempotent_refs for QoS 1 delivery", adapter.ErrUnsupported)
	}
	cfg.TopicPrefix = strings.Trim(cfg.TopicPrefix, "/")
	if cfg.TopicPrefix == "" || strings.ContainsAny(cfg.TopicPrefix, "+#") {
		return nil, fmt.Errorf("mqtt topic prefix %q must be non-empty and free of wildcards", cfg.TopicPrefix)
	}
	a := &Adapter{cfg: cfg, transport: transport, now: time.Now, waiting: make(map[string]waiter)}
	if err := transport.Subscribe(ctx, cfg.TopicPrefix+"/+/command/reply", a.deliver); err != nil {
		return nil, fmt.Errorf("subscribe to meter replies: %w", err)
	}
	if cfg.Capabilities.Capabilities.OutcomeQuery {
		return reconcilingAdapter{a}, nil
	}
	return a, nil
}

func (a *Adapter) Capabilities() adapter.Capabilities {
	return a.cfg.Capabilities
}

func (a *Adapter) Execute(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return adapter.CommandResult{}, adapter.ContextError(err)
	}
	if err := a.cfg.Capabilities.Supports(cmd); err != nil {
		return adapter.CommandResult{}, err
	}
	if err := cmd.Validate(); err != nil {
		return adapter.CommandResult{}, err
	}
	if err := topicSafe(cmd.DeviceID); err != nil {
		return adapter.CommandResult{}, err
	}
	got, err := a.exchange(ctx, cmd.DeviceID, encode(cmd), true)
	if err != nil {
		return adapter.CommandResult{}, err
	}
	if !got.OK {
		return adapter.CommandResult{}, fmt.Errorf("%w: %s: %s", adapter.ErrDeviceRejected, got.Code, got.Message)
	}
	return a.result(cmd.ID, got), nil
}

func (r reconcilingAdapter) Outcome(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, bool, error) {
	if err := topicSafe(cmd.DeviceID); err != nil {
		return adapter.CommandResult{}, false, err
	}
	query := request{Ref: cmd.ID + ":outcome:" + fmt.Sprint(r.now().UnixNano()), Op: "outcome", Device: cmd.DeviceID, Target: cmd.ID}
	got, err := r.exchange(ctx, cmd.DeviceID, query, false)
	if err != nil {
		return adapter.CommandResult{}, false, err
	}
	if !got.OK || got.Applied == nil {
		return adapter.CommandResult{}, false, fmt.Errorf("%w: outcome query for %q answered %s: %s", adapter.ErrInvalidResponse, cmd.ID, got.Code, got.Message)
	}
	if !*got.Applied {
		return adapter.CommandResult{}, false, nil
	}
	return r.result(cmd.ID, got), true, nil
}

// exchange publishes req and waits for the reply carrying its reference. Only
// commands mark the send through adapter.BeforeSend; an outcome query changes
// nothing on the device.
func (a *Adapter) exchange(ctx context.Context, deviceID string, req request, command bool) (reply, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return reply{}, fmt.Errorf("encode %s request: %w", req.Op, err)
	}
	commandTopic := a.cfg.TopicPrefix + "/" + deviceID + "/command"
	replies, err := a.await(req.Ref, commandTopic+"/reply")
	if err != nil {
		return reply{}, err
	}
	defer a.forget(req.Ref)

	var beforeSend func() error
	if command {
		beforeSend = func() error { return adapter.BeforeSend(ctx) }
	}
	if err := a.transport.Publish(ctx, commandTopic, payload, beforeSend); err != nil {
		return reply{}, adapter.ContextError(fmt.Errorf("publish %s to %s: %w", req.Op, commandTopic, err))
	}
	select {
	case got := <-replies:
		return got, nil
	case <-ctx.Done():
		return reply{}, adapter.ContextError(ctx.Err())
	}
}

func (a *Adapter) await(ref, replyTopic string) (chan reply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, busy := a.waiting[ref]; busy {
		return nil, fmt.Errorf("%w: reference %q is already waiting for a reply", adapter.ErrInvalidCommand, ref)
	}
	replies := make(chan reply, 1)
	a.waiting[ref] = waiter{replyTopic: replyTopic, replies: replies}
	return replies, nil
}

func (a *Adapter) forget(ref string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.waiting, ref)
}

// deliver runs on the transport's callback goroutine and must not block.
func (a *Adapter) deliver(topic string, payload []byte) {
	var got reply
	if err := json.Unmarshal(payload, &got); err != nil || got.Ref == "" {
		slog.Warn("discard malformed meter reply", "topic", topic, "error", err)
		return
	}
	a.mu.Lock()
	w, ok := a.waiting[got.Ref]
	a.mu.Unlock()
	switch {
	case !ok:
		slog.Debug("discard meter reply nobody is waiting for", "topic", topic, "ref", got.Ref)
	case w.replyTopic != topic:
		slog.Warn("discard meter reply from the wrong device", "topic", topic, "want", w.replyTopic, "ref", got.Ref)
	default:
		select {
		case w.replies <- got:
		default: // a duplicate reply; the first one already settled the exchange
		}
	}
}

func (a *Adapter) result(commandID string, got reply) adapter.CommandResult {
	result := adapter.CommandResult{CommandID: commandID, AcknowledgedAt: time.UnixMilli(got.TsMs).UTC()}
	if got.TsMs == 0 {
		result.AcknowledgedAt = a.now().UTC()
	}
	if got.Relay != nil {
		closed := *got.Relay == 1
		result.RelayClosed = &closed
	}
	if got.BalanceWh != nil {
		kwh := float64(*got.BalanceWh) / 1000
		result.BalanceKWh = &kwh
	}
	if got.Tamper != nil {
		tamper := *got.Tamper == 1
		result.TamperDetected = &tamper
	}
	return result
}

func encode(cmd adapter.Command) request {
	req := request{Ref: cmd.ID, Device: cmd.DeviceID}
	switch cmd.Type {
	case adapter.CommandCredit:
		req.Op = "credit"
		if cmd.STSToken != "" {
			req.Token = cmd.STSToken
		} else {
			req.Wh = int64(math.Round(cmd.CreditKWh * 1000))
		}
	case adapter.CommandDisconnect, adapter.CommandReconnect:
		closed := cmd.Type == adapter.CommandReconnect
		req.Op, req.Closed = "relay", &closed
	case adapter.CommandRequestStatus:
		req.Op = "status"
	}
	return req
}

var errUnsafeDevice = errors.New("device ID cannot be used in an MQTT topic")

func topicSafe(deviceID string) error {
	if deviceID == "" || strings.ContainsAny(deviceID, "/+#") {
		return fmt.Errorf("%w: %w: %q", adapter.ErrInvalidCommand, errUnsafeDevice, deviceID)
	}
	return nil
}
