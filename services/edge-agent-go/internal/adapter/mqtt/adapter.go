// mqtt carries meter commands to devices as MQTT v5 requests, using the
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
	"sync/atomic"
	"time"

	"edge-agent-go/internal/adapter"
)

const DefaultResponseTimeout = 30 * time.Second

// Message is an MQTT v5 publication. Requests carry ResponseTopic and
// CorrelationData; a reply echoes the request's CorrelationData.
type Message struct {
	Topic           string
	Payload         []byte
	ResponseTopic   string
	CorrelationData []byte
}

// Transport is the broker connection. Publish calls beforeSend, when non-nil,
// after its readiness checks and immediately before handing a command off; an
// error from it forbids publication. Deliver callbacks must return quickly.
type Transport interface {
	Publish(ctx context.Context, msg Message, beforeSend func() error) error
	Subscribe(ctx context.Context, filter string, deliver func(Message)) error
}

type Config struct {
	// DeviceID binds an inventory adapter to one device; empty keeps the legacy site-wide mode.
	DeviceID     string
	Topics       Topics
	Capabilities adapter.Capabilities
	// ResponseTimeout bounds the wait for a device reply. A PUBACK only means
	// the broker has the request, never that the device acted on it.
	ResponseTimeout time.Duration
}

// request is the payload published to the command topic.
type request struct {
	Op     string `json:"op"`
	Device string `json:"device"`
	Wh     int64  `json:"wh,omitempty"`
	Token  string `json:"token,omitempty"`
	Closed *bool  `json:"closed,omitempty"`
	// Target is the command ID an "outcome" query asks about.
	Target string `json:"target,omitempty"`
}

type reply struct {
	OK        *bool  `json:"ok"`
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

// New subscribes to device replies before any request is published, and
// returns an adapter.CommandAdapter that also implements adapter.Reconciler
// when the declaration has outcome_query.
func New(ctx context.Context, cfg Config, transport Transport) (adapter.CommandAdapter, error) {
	// QoS 1 can redeliver below the platform ledger. Monetary credits need
	// receiver-side deduplication of the correlation data even for one attempt.
	if cfg.Capabilities.Capabilities.PrepaidCredit && !cfg.Capabilities.Capabilities.IdempotentRefs {
		return nil, fmt.Errorf("%w: MQTT prepaid credit requires idempotent_refs for QoS 1 delivery", adapter.ErrUnsupported)
	}
	if cfg.Topics == (Topics{}) {
		return nil, errors.New("mqtt topic map is required")
	}
	if cfg.ResponseTimeout <= 0 {
		cfg.ResponseTimeout = DefaultResponseTimeout
	}
	a := &Adapter{cfg: cfg, transport: transport, now: time.Now, waiting: make(map[string]waiter)}
	filter := cfg.Topics.ReplyFilter()
	if cfg.DeviceID != "" {
		if err := topicSafe(cfg.DeviceID); err != nil {
			return nil, err
		}
		filter = cfg.Topics.Reply(cfg.DeviceID)
	}
	if err := transport.Subscribe(ctx, filter, a.deliver); err != nil {
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
	if a.cfg.DeviceID != "" && cmd.DeviceID != a.cfg.DeviceID {
		return adapter.CommandResult{}, fmt.Errorf("%w: adapter belongs to device %q", adapter.ErrUnsupported, a.cfg.DeviceID)
	}
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
	// The command ID is the correlation data, so a retry is the same request
	// to a device that deduplicates.
	got, err := a.exchange(ctx, cmd.DeviceID, cmd.ID, encode(cmd), true)
	if err != nil {
		return adapter.CommandResult{}, err
	}
	if !*got.OK {
		return adapter.CommandResult{}, fmt.Errorf("%w: %s: %s", adapter.ErrDeviceRejected, got.Code, got.Message)
	}
	return a.result(cmd.ID, got), nil
}

func (r reconcilingAdapter) Outcome(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, bool, error) {
	if r.cfg.DeviceID != "" && cmd.DeviceID != r.cfg.DeviceID {
		return adapter.CommandResult{}, false, fmt.Errorf("%w: adapter belongs to device %q", adapter.ErrUnsupported, r.cfg.DeviceID)
	}
	if err := topicSafe(cmd.DeviceID); err != nil {
		return adapter.CommandResult{}, false, err
	}
	correlation := cmd.ID + ":outcome:" + fmt.Sprint(r.now().UnixNano())
	got, err := r.exchange(ctx, cmd.DeviceID, correlation, request{Op: "outcome", Device: cmd.DeviceID, Target: cmd.ID}, false)
	if err != nil {
		return adapter.CommandResult{}, false, err
	}
	if !*got.OK || got.Applied == nil {
		return adapter.CommandResult{}, false, fmt.Errorf("%w: outcome query for %q answered %s: %s", adapter.ErrInvalidResponse, cmd.ID, got.Code, got.Message)
	}
	if !*got.Applied {
		return adapter.CommandResult{}, false, nil
	}
	return r.result(cmd.ID, got), true, nil
}

// exchange publishes req and waits for the reply carrying its correlation
// data. Only commands mark the send through adapter.BeforeSend; an outcome
// query changes nothing on the device.
func (a *Adapter) exchange(ctx context.Context, deviceID, correlation string, req request, command bool) (reply, error) {
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, a.cfg.ResponseTimeout)
	defer cancel()
	payload, err := json.Marshal(req)
	if err != nil {
		return reply{}, fmt.Errorf("encode %s request: %w", req.Op, err)
	}
	replyTopic := a.cfg.Topics.Reply(deviceID)
	replies, err := a.await(correlation, replyTopic)
	if err != nil {
		return reply{}, err
	}
	defer a.forget(correlation)

	var handedOff atomic.Bool
	beforeSend := func() error {
		if command {
			if err := adapter.BeforeSend(ctx); err != nil {
				return err
			}
		}
		handedOff.Store(true)
		return nil
	}
	msg := Message{Topic: a.cfg.Topics.Command(deviceID), Payload: payload, ResponseTopic: replyTopic, CorrelationData: []byte(correlation)}
	if err := a.transport.Publish(ctx, msg, beforeSend); err != nil {
		if handedOff.Load() && adapter.ProtocolDeadlineExceeded(caller, ctx) {
			return reply{}, fmt.Errorf("%w: %w", adapter.ErrProtocolTimeout, err)
		}
		return reply{}, adapter.ContextError(fmt.Errorf("publish %s to %s: %w", req.Op, msg.Topic, err))
	}
	select {
	case got := <-replies:
		if got.OK == nil {
			return reply{}, fmt.Errorf("%w: reply to %q has no ok outcome", adapter.ErrInvalidResponse, correlation)
		}
		return got, nil
	case <-ctx.Done():
		if handedOff.Load() && adapter.ProtocolDeadlineExceeded(caller, ctx) {
			return reply{}, fmt.Errorf("%w: %w", adapter.ErrProtocolTimeout, ctx.Err())
		}
		return reply{}, adapter.ContextError(ctx.Err())
	}
}

func (a *Adapter) await(correlation, replyTopic string) (chan reply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, busy := a.waiting[correlation]; busy {
		return nil, fmt.Errorf("%w: %q is already waiting for a reply", adapter.ErrInvalidCommand, correlation)
	}
	replies := make(chan reply, 1)
	a.waiting[correlation] = waiter{replyTopic: replyTopic, replies: replies}
	return replies, nil
}

func (a *Adapter) forget(correlation string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.waiting, correlation)
}

// deliver runs on the transport's callback goroutine and must not block.
func (a *Adapter) deliver(msg Message) {
	correlation := string(msg.CorrelationData)
	a.mu.Lock()
	w, ok := a.waiting[correlation]
	a.mu.Unlock()
	switch {
	case !ok:
		slog.Debug("discard meter reply nobody is waiting for", "topic", msg.Topic, "correlation", correlation)
		return
	// Broker access rules stop a device publishing on another device's reply
	// topic, so the topic, not the guessable command ID, says who answered.
	case w.replyTopic != msg.Topic:
		slog.Warn("discard meter reply from the wrong device", "topic", msg.Topic, "want", w.replyTopic, "correlation", correlation)
		return
	}
	var got reply
	if err := json.Unmarshal(msg.Payload, &got); err != nil {
		slog.Warn("discard malformed meter reply", "topic", msg.Topic, "error", err)
		return
	}
	select {
	case w.replies <- got:
	default: // a duplicate reply; the first one already settled the exchange
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
	req := request{Device: cmd.DeviceID}
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
