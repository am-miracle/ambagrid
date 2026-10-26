package mqtt_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/adaptertest"
	"edge-agent-go/internal/adapter/devicetest"
	"edge-agent-go/internal/adapter/mqtt"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

const siteID = "site-01"

var meterCaps = adapter.Capabilities{
	DeviceType: "smart_meter", Manufacturer: "test", Model: "mqtt-meter",
	Capabilities: adapter.CapabilitySet{Telemetry: true, RelayControl: true, PrepaidCredit: true, IdempotentRefs: true},
}

func testTopics(t *testing.T) mqtt.Topics {
	t.Helper()
	topics, err := mqtt.NewTopics(mqtt.DefaultCommandTopic, mqtt.DefaultReplyTopic, siteID)
	if err != nil {
		t.Fatal(err)
	}
	return topics
}

// memoryBroker delivers each message on its own goroutine, as a real client
// library does, so a held device never blocks the publisher.
type memoryBroker struct {
	mu           sync.Mutex
	subs         []subscription
	published    []mqtt.Message
	disconnected bool
}

type subscription struct {
	filter  string
	deliver func(mqtt.Message)
}

func (b *memoryBroker) Publish(ctx context.Context, msg mqtt.Message, beforeSend func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	disconnected := b.disconnected
	b.mu.Unlock()
	if disconnected {
		return errors.New("broker connection is down")
	}
	if beforeSend != nil {
		if err := beforeSend(); err != nil {
			return err
		}
	}
	b.mu.Lock()
	b.published = append(b.published, msg)
	subs := append([]subscription(nil), b.subs...)
	b.mu.Unlock()
	for _, s := range subs {
		if topicMatches(s.filter, msg.Topic) {
			copied := msg
			copied.Payload = append([]byte(nil), msg.Payload...)
			go s.deliver(copied)
		}
	}
	return nil
}

func (b *memoryBroker) Subscribe(_ context.Context, filter string, deliver func(mqtt.Message)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, subscription{filter, deliver})
	return nil
}

func topicMatches(filter, topic string) bool {
	f, n := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, level := range f {
		if level == "#" {
			return true
		}
		if i >= len(n) || (level != "+" && level != n[i]) {
			return false
		}
	}
	return len(f) == len(n)
}

// The test keeps its own copy of the payloads, so these tests pin the protocol
// rather than whatever the adapter happens to send.
type wireRequest struct {
	Op     string `json:"op"`
	Device string `json:"device"`
	Wh     int64  `json:"wh"`
	Closed *bool  `json:"closed"`
	Target string `json:"target"`
}

type wireReply struct {
	OK        bool   `json:"ok"`
	TsMs      int64  `json:"ts_ms,omitempty"`
	Relay     *int   `json:"relay,omitempty"`
	BalanceWh *int64 `json:"balance_wh,omitempty"`
	Applied   *bool  `json:"applied,omitempty"`
}

// meterDevice is a meter on the far side of the broker. It deduplicates by
// correlation data, as a device declaring idempotent_refs must.
type meterDevice struct {
	devicetest.Gate
	devicetest.Recorder

	ctx       context.Context
	transport mqtt.Transport

	mu          sync.Mutex
	rejection   []byte
	dropReplies int
	// spoofDevice, when set, answers on that device's reply topic instead.
	spoofDevice string
	topics      mqtt.Topics
	relayClosed bool
	balanceWh   int64
	replies     map[string][]byte
	commands    int
}

func newMeterDevice(t *testing.T, transport mqtt.Transport) *meterDevice {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &meterDevice{ctx: ctx, transport: transport, topics: testTopics(t), relayClosed: true, replies: make(map[string][]byte)}
	// Transport callbacks must return quickly, so the meter works off the
	// callback goroutine, as a real device's MQTT client would.
	if err := transport.Subscribe(ctx, testTopics(t).Command("+"), func(msg mqtt.Message) { go d.handle(msg) }); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *meterDevice) Reject(response []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rejection = response
}

func (d *meterDevice) commandsReceived() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.commands
}

func (d *meterDevice) handle(msg mqtt.Message) {
	var req wireRequest
	if err := json.Unmarshal(msg.Payload, &req); err != nil || len(msg.CorrelationData) == 0 || msg.ResponseTopic == "" {
		return
	}
	if err := d.Enter(d.ctx); err != nil {
		return
	}
	correlation := string(msg.CorrelationData)
	d.mu.Lock()
	var out []byte
	switch {
	case req.Op == "outcome":
		_, applied := d.replies[req.Target]
		out, _ = json.Marshal(wireReply{OK: true, Applied: &applied, TsMs: time.Now().UnixMilli()})
	case d.replies[correlation] != nil:
		d.commands++
		out = d.replies[correlation]
	case d.rejection != nil:
		d.commands++
		out, d.rejection = d.rejection, nil
	default:
		d.commands++
		switch req.Op {
		case "credit":
			d.balanceWh += req.Wh
		case "relay":
			d.relayClosed = *req.Closed
		}
		d.Record(msg.Payload)
		relay, balance := 0, d.balanceWh
		if d.relayClosed {
			relay = 1
		}
		out, _ = json.Marshal(wireReply{OK: true, TsMs: time.Now().UnixMilli(), Relay: &relay, BalanceWh: &balance})
		d.replies[correlation] = out
	}
	drop := req.Op != "outcome" && d.dropReplies > 0
	if drop {
		d.dropReplies--
	}
	replyTopic := msg.ResponseTopic
	if d.spoofDevice != "" {
		replyTopic = d.topics.Reply(d.spoofDevice)
	}
	d.mu.Unlock()
	if !drop {
		_ = d.transport.Publish(d.ctx, mqtt.Message{Topic: replyTopic, Payload: out, CorrelationData: msg.CorrelationData}, nil)
	}
}

func ledgerStore(t *testing.T) *queuesqlite.Store {
	t.Helper()
	store, err := queuesqlite.Open(context.Background(), queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: siteID, GatewayID: "gateway-01",
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
	a, err := mqtt.New(context.Background(), mqtt.Config{Topics: testTopics(t), Capabilities: caps, ResponseTimeout: 2 * time.Second}, transport)
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

func execute(a adapter.CommandAdapter, cmd adapter.Command) (adapter.CommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return a.Execute(ctx, cmd)
}

func TestRequestCarriesPerDeviceResponseTopicAndCommandCorrelation(t *testing.T) {
	broker := &memoryBroker{}
	newMeterDevice(t, broker)
	a := newAdapter(t, broker, meterCaps)
	if _, err := execute(a, adapter.Command{ID: "cmd-0101", Type: adapter.CommandDisconnect, DeviceID: "meter-01"}); err != nil {
		t.Fatal(err)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	request := broker.published[0]
	if request.Topic != "site/site-01/meter/meter-01/cmd" || request.ResponseTopic != "site/site-01/meter/meter-01/cmd/reply" || string(request.CorrelationData) != "cmd-0101" {
		t.Fatalf("request = topic %q response %q correlation %q", request.Topic, request.ResponseTopic, request.CorrelationData)
	}
}

func TestRepliesFromAnotherDeviceAreIgnored(t *testing.T) {
	broker := &memoryBroker{}
	device := newMeterDevice(t, broker)
	device.spoofDevice = "meter-99"
	a := newAdapter(t, broker, meterCaps)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.Execute(ctx, adapter.Command{ID: "cmd-1", Type: adapter.CommandDisconnect, DeviceID: "meter-01"})
	if !errors.Is(err, adapter.ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout because only another device's topic answered", err)
	}
}

func TestAdapterEnforcesItsOwnResponseTimeout(t *testing.T) {
	broker := &memoryBroker{}
	device := newMeterDevice(t, broker)
	device.dropReplies = 1
	a, err := mqtt.New(context.Background(), mqtt.Config{Topics: testTopics(t), Capabilities: meterCaps, ResponseTimeout: 50 * time.Millisecond}, broker)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		// No caller deadline: only the adapter's response timeout can end the wait.
		_, err := a.Execute(context.Background(), adapter.Command{ID: "cmd-1", Type: adapter.CommandDisconnect, DeviceID: "meter-01"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, adapter.ErrTimeout) {
			t.Fatalf("error = %v, want ErrTimeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("adapter kept waiting for a reply with no response timeout")
	}
}

func TestDisconnectedBrokerDoesNotMarkCommandSent(t *testing.T) {
	broker := &memoryBroker{disconnected: true}
	a := newAdapter(t, broker, meterCaps)
	marked := 0
	ctx := adapter.WithSendHook(context.Background(), func() error { marked++; return nil })
	if _, err := a.Execute(ctx, adapter.Command{ID: "cmd-1", Type: adapter.CommandDisconnect, DeviceID: "meter-01"}); err == nil {
		t.Fatal("command succeeded over a disconnected broker")
	}
	if marked != 0 {
		t.Fatalf("unsent command marked sent %d times", marked)
	}
}

func TestDeviceIDsThatBreakTopicsAreRejected(t *testing.T) {
	a := newAdapter(t, &memoryBroker{}, meterCaps)
	for _, id := range []string{"meter/01", "meter+", "meter#"} {
		if _, err := execute(a, adapter.Command{ID: "cmd-1", Type: adapter.CommandDisconnect, DeviceID: id}); !errors.Is(err, adapter.ErrInvalidCommand) {
			t.Errorf("device %q: error = %v, want ErrInvalidCommand", id, err)
		}
	}
}

func TestLostCreditReplyIsNeverAppliedTwice(t *testing.T) {
	credit := adapter.Command{ID: "cmd-credit", Type: adapter.CommandCredit, DeviceID: "meter-01", CreditKWh: 12.5}
	withQuery := meterCaps
	withQuery.Capabilities.OutcomeQuery = true
	for name, c := range map[string]struct {
		caps         adapter.Capabilities
		wantCommands int
	}{
		"outcome query settles it without resending": {caps: withQuery, wantCommands: 1},
		"resent and deduplicated by the device":      {caps: meterCaps, wantCommands: 2},
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
			if result, err := execute(ledger, credit); err != nil || result.CommandID != credit.ID {
				t.Fatalf("retry = %+v, %v", result, err)
			}
			if n := len(device.Received()); n != 1 {
				t.Fatalf("meter applied the credit %d times, want once", n)
			}
			if n := device.commandsReceived(); n != c.wantCommands {
				t.Fatalf("meter received the command %d times, want %d", n, c.wantCommands)
			}
		})
	}
}

func TestMQTTCreditRequiresReceiverDeduplication(t *testing.T) {
	caps := meterCaps
	caps.Capabilities.IdempotentRefs = false
	_, err := mqtt.New(context.Background(), mqtt.Config{Topics: testTopics(t), Capabilities: caps}, &memoryBroker{})
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("error = %v, want unsafe credit rejected", err)
	}
}
