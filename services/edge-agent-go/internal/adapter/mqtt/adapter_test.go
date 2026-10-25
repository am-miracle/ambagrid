package mqtt_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/adaptertest"
	"edge-agent-go/internal/adapter/devicetest"
	"edge-agent-go/internal/adapter/mqtt"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

const prefix = "ambagrid/site-01"

var meterCaps = adapter.Capabilities{
	DeviceType: "smart_meter", Manufacturer: "test", Model: "mqtt-meter",
	Capabilities: adapter.CapabilitySet{Telemetry: true, RelayControl: true, PrepaidCredit: true, IdempotentRefs: true},
}

// memoryBroker delivers each message on its own goroutine, as a real client
// library does, so a held device never blocks the publisher.
type memoryBroker struct {
	mu   sync.Mutex
	subs []subscription
}

type subscription struct {
	filter  string
	deliver func(string, []byte)
}

func (b *memoryBroker) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	subs := append([]subscription(nil), b.subs...)
	b.mu.Unlock()
	for _, s := range subs {
		if matches(s.filter, topic) {
			go s.deliver(topic, append([]byte(nil), payload...))
		}
	}
	return nil
}

func (b *memoryBroker) Subscribe(_ context.Context, filter string, deliver func(string, []byte)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, subscription{filter, deliver})
	return nil
}

func matches(filter, topic string) bool {
	f, t := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, part := range f {
		if part == "#" {
			return true
		}
		if i >= len(t) || (part != "+" && part != t[i]) {
			return false
		}
	}
	return len(f) == len(t)
}

// The test keeps its own copy of the wire format, so these tests pin the
// protocol rather than whatever the adapter happens to send.
type wireRequest struct {
	Ref    string `json:"ref"`
	Op     string `json:"op"`
	Device string `json:"device"`
	Wh     int64  `json:"wh"`
	Closed *bool  `json:"closed"`
	Target string `json:"target"`
}

type wireReply struct {
	Ref       string `json:"ref"`
	OK        bool   `json:"ok"`
	TsMs      int64  `json:"ts_ms,omitempty"`
	Relay     *int   `json:"relay,omitempty"`
	BalanceWh *int64 `json:"balance_wh,omitempty"`
	Applied   *bool  `json:"applied,omitempty"`
}

// meterDevice is a meter on the far side of the broker.
type meterDevice struct {
	devicetest.Gate
	devicetest.Recorder

	ctx       context.Context
	transport mqtt.Transport

	mu          sync.Mutex
	rejection   []byte
	dropReplies int
	replyTopic  func(string) string
	relayClosed bool
	balanceWh   int64
	applied     map[string]bool
}

func newMeterDevice(t *testing.T, transport mqtt.Transport) *meterDevice {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &meterDevice{ctx: ctx, transport: transport, relayClosed: true, applied: make(map[string]bool)}
	if err := transport.Subscribe(ctx, prefix+"/+/command", d.handle); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *meterDevice) Reject(response []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rejection = response
}

func (d *meterDevice) handle(topic string, payload []byte) {
	var req wireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}
	if err := d.Enter(d.ctx); err != nil {
		return
	}
	d.mu.Lock()
	replyTopic := topic + "/reply"
	if d.replyTopic != nil {
		replyTopic = d.replyTopic(topic)
	}
	var out []byte
	switch {
	case req.Op == "outcome":
		applied := d.applied[req.Target]
		out, _ = json.Marshal(wireReply{Ref: req.Ref, OK: true, Applied: &applied, TsMs: time.Now().UnixMilli()})
	case d.rejection != nil:
		out, d.rejection = d.rejection, nil
	default:
		if !d.applied[req.Ref] {
			switch req.Op {
			case "credit":
				d.balanceWh += req.Wh
			case "relay":
				d.relayClosed = *req.Closed
			}
			d.Record(payload)
			d.applied[req.Ref] = true
		}
		relay, balance := 0, d.balanceWh
		if d.relayClosed {
			relay = 1
		}
		out, _ = json.Marshal(wireReply{Ref: req.Ref, OK: true, TsMs: time.Now().UnixMilli(), Relay: &relay, BalanceWh: &balance})
	}
	drop := req.Op != "outcome" && d.dropReplies > 0
	if drop {
		d.dropReplies--
	}
	d.mu.Unlock()
	if !drop {
		_ = d.transport.Publish(d.ctx, replyTopic, out)
	}
}

func ledgerStore(t *testing.T) *queuesqlite.Store {
	t.Helper()
	store, err := queuesqlite.Open(context.Background(), queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newAdapter(t *testing.T, transport mqtt.Transport, caps adapter.Capabilities) adapter.CommandAdapter {
	t.Helper()
	a, err := mqtt.New(context.Background(), mqtt.Config{TopicPrefix: prefix, Capabilities: caps}, transport)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func runConformance(t *testing.T, transports func(t *testing.T) (adapterSide, deviceSide mqtt.Transport)) {
	withQuery := meterCaps
	withQuery.Capabilities.OutcomeQuery = true
	for name, caps := range map[string]adapter.Capabilities{"Plain": meterCaps, "OutcomeQuery": withQuery} {
		t.Run(name, func(t *testing.T) {
			adaptertest.RunCommand(t, func(t *testing.T) adaptertest.CommandHarness {
				adapterSide, deviceSide := transports(t)
				device := newMeterDevice(t, deviceSide)
				return adaptertest.CommandHarness{
					Adapter:  adapter.NewLedger(ledgerStore(t), newAdapter(t, adapterSide, caps)),
					Device:   device,
					Fixtures: os.DirFS("../testdata/mqtt"),
				}
			})
		})
	}
}

func TestCommandConformance(t *testing.T) {
	runConformance(t, func(*testing.T) (mqtt.Transport, mqtt.Transport) {
		broker := &memoryBroker{}
		return broker, broker
	})
}

func execute(ctx context.Context, a adapter.CommandAdapter, cmd adapter.Command) (adapter.CommandResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return a.Execute(ctx, cmd)
}

func TestRepliesFromAnotherDeviceAreIgnored(t *testing.T) {
	broker := &memoryBroker{}
	device := newMeterDevice(t, broker)
	device.replyTopic = func(string) string { return prefix + "/meter-99/command/reply" }
	a := newAdapter(t, broker, meterCaps)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.Execute(ctx, adapter.Command{ID: "cmd-1", Type: adapter.CommandDisconnect, DeviceID: "meter-01"})
	if !errors.Is(err, adapter.ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout because only another device's topic answered", err)
	}
}

func TestDeviceIDsThatBreakTopicsAreRejected(t *testing.T) {
	broker := &memoryBroker{}
	a := newAdapter(t, broker, meterCaps)
	for _, id := range []string{"meter/01", "meter+", "meter#"} {
		_, err := execute(context.Background(), a, adapter.Command{ID: "cmd-1", Type: adapter.CommandDisconnect, DeviceID: id})
		if !errors.Is(err, adapter.ErrInvalidCommand) {
			t.Errorf("device %q: error = %v, want ErrInvalidCommand", id, err)
		}
	}
	if _, err := mqtt.New(context.Background(), mqtt.Config{TopicPrefix: "ambagrid/+", Capabilities: meterCaps}, broker); err == nil {
		t.Error("accepted a wildcard topic prefix")
	}
}

func TestLostCreditReplyIsSettledByOutcomeQueryWithoutResending(t *testing.T) {
	credit := adapter.Command{ID: "cmd-credit", Type: adapter.CommandCredit, DeviceID: "meter-01", CreditKWh: 12.5}
	withQuery := meterCaps
	withQuery.Capabilities.OutcomeQuery = true
	for name, c := range map[string]struct {
		caps    adapter.Capabilities
		wantErr error
	}{
		"device answers outcome queries": {caps: withQuery},
		"device deduplicates references": {caps: meterCaps},
	} {
		t.Run(name, func(t *testing.T) {
			broker := &memoryBroker{}
			device := newMeterDevice(t, broker)
			device.dropReplies = 1
			ledger := adapter.NewLedger(ledgerStore(t), newAdapter(t, broker, c.caps))

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err := ledger.Execute(ctx, credit)
			cancel()
			if !errors.Is(err, adapter.ErrOutcomeUnknown) {
				t.Fatalf("lost reply error = %v, want ErrOutcomeUnknown", err)
			}
			result, err := execute(context.Background(), ledger, credit)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("retry error = %v, want %v", err, c.wantErr)
				}
			} else if err != nil || result.CommandID != credit.ID {
				t.Fatalf("retry = %+v, %v; want the outcome query to confirm the credit", result, err)
			}
			if n := len(device.Received()); n != 1 {
				t.Fatalf("meter applied the credit %d times, want once", n)
			}
		})
	}
}

// TestCommandConformanceOverMosquitto runs the suite through paho and a real
// broker, e.g. EDGE_MQTT_TEST_BROKER=tcp://127.0.0.1:1883.
func TestCommandConformanceOverMosquitto(t *testing.T) {
	broker := os.Getenv("EDGE_MQTT_TEST_BROKER")
	if broker == "" {
		t.Skip("set EDGE_MQTT_TEST_BROKER to run against a real broker")
	}
	var clients atomic.Int64
	dial := func(t *testing.T, role string) mqtt.Transport {
		// Brokers drop an existing session when a client ID is reused.
		id := fmt.Sprintf("%s-%d-%d", role, os.Getpid(), clients.Add(1))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		transport, err := mqtt.DialPaho(ctx, mqtt.PahoConfig{Broker: broker, ClientID: id})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(transport.Close)
		return transport
	}
	runConformance(t, func(t *testing.T) (mqtt.Transport, mqtt.Transport) {
		return dial(t, "edge"), dial(t, "meter")
	})
}

func TestMQTTCreditRequiresReceiverDeduplication(t *testing.T) {
	caps := meterCaps
	caps.Capabilities.IdempotentRefs = false
	_, err := mqtt.New(context.Background(), mqtt.Config{TopicPrefix: prefix, Capabilities: caps}, &memoryBroker{})
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("error=%v, want unsafe credit rejected", err)
	}
}
