package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"ingestion-go/internal/config"
	"ingestion-go/internal/stats"
)

func TestQueueGuardCloseOnceClosesQueueAndRejectsNewMessages(t *testing.T) {
	queue := newQueueGuard(1)

	if !queue.enqueue(envelope{Topic: "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry"}) {
		t.Fatal("enqueue returned false before queue was closed")
	}

	queue.closeOnce()
	queue.closeOnce()

	if queue.enqueue(envelope{Topic: "africa-west/ng-kaji-01/smartmeter/met-0102/telemetry"}) {
		t.Fatal("enqueue returned true after queue was closed")
	}

	got, ok := <-queue.queue
	if !ok {
		t.Fatal("queue was closed before draining the accepted message")
	}
	if got.Topic != "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry" {
		t.Fatalf("Topic = %q, want accepted message", got.Topic)
	}
	if _, ok := <-queue.queue; ok {
		t.Fatal("queue remained open after closeOnce")
	}
}

func TestQueueGuardConcurrentCloseAndEnqueueDoesNotPanic(t *testing.T) {
	queue := newQueueGuard(64)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = queue.enqueue(envelope{Topic: "topic"})
		}()
	}

	close(start)
	queue.closeOnce()
	wg.Wait()

	if queue.enqueue(envelope{Topic: "topic"}) {
		t.Fatal("enqueue returned true after concurrent close")
	}
}

func TestDefaultPublishHandlerCopiesPayloadAndTracksDrops(t *testing.T) {
	queue := newQueueGuard(1)
	var st stats.IngestionStats
	subscribed := make(chan error, 1)
	opts := mqttClientOptions(testConfig(), queue, &st, subscribed)

	payload := []byte(`{"device_id":"met-0101"}`)
	opts.DefaultPublishHandler(nil, fakeMessage{
		topic:     "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry",
		payload:   payload,
		duplicate: true,
		qos:       1,
		retained:  true,
		messageID: 42,
	})
	payload[0] = '['

	got := <-queue.queue
	if string(got.Payload) != `{"device_id":"met-0101"}` {
		t.Fatalf("Payload = %q, want immutable copy of original payload", got.Payload)
	}
	if got.Metadata.Duplicate != true || got.Metadata.QOS != 1 || got.Metadata.Retained != true || got.Metadata.MessageID != 42 {
		t.Fatalf("Metadata = %+v, want MQTT metadata copied from message", got.Metadata)
	}

	opts.DefaultPublishHandler(nil, fakeMessage{
		topic:   "africa-west/ng-kaji-01/smartmeter/met-0102/telemetry",
		payload: []byte(`{"device_id":"met-0102"}`),
	})

	if st.Received() != 2 {
		t.Fatalf("Received = %d, want 2", st.Received())
	}
	if st.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0 after draining first message", st.Dropped())
	}

	opts.DefaultPublishHandler(nil, fakeMessage{
		topic:   "africa-west/ng-kaji-01/smartmeter/met-0103/telemetry",
		payload: []byte(`{"device_id":"met-0103"}`),
	})

	if st.Received() != 3 {
		t.Fatalf("Received = %d, want 3", st.Received())
	}
	if st.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1 for full queue", st.Dropped())
	}
}

func TestWaitForMQTTConnectReturnsContextErrorBeforeTokenCompletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pending := make(chan struct{})
	cancel()

	err := waitForMQTTConnect(ctx, fakeToken{done: pending}, "tcp://mqtt:1883")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForMQTTConnect error = %v, want context.Canceled", err)
	}
}

func TestWaitForMQTTConnectWrapsTokenError(t *testing.T) {
	wantErr := errors.New("dial failed")

	err := waitForMQTTConnect(context.Background(), fakeToken{err: wantErr}, "tcp://mqtt:1883")

	if !errors.Is(err, wantErr) {
		t.Fatalf("waitForMQTTConnect error = %v, want %v", err, wantErr)
	}
	if err == nil || !strings.Contains(err.Error(), "tcp://mqtt:1883") {
		t.Fatalf("waitForMQTTConnect error = %v, want broker address in error", err)
	}
}

func TestWaitForMQTTConnectReturnsNilOnSuccess(t *testing.T) {
	if err := waitForMQTTConnect(context.Background(), fakeToken{}, "tcp://mqtt:1883"); err != nil {
		t.Fatalf("waitForMQTTConnect error = %v, want nil", err)
	}
}

func TestResubscribeReportsOnlyFirstAttempt(t *testing.T) {
	client := &fakeClient{tokens: []mqtt.Token{
		fakeToken{},
		fakeToken{err: errors.New("second attempt failed")},
	}}
	subscribed := make(chan error, 2)
	var once sync.Once

	resubscribe(client, testConfig(), subscribed, &once)
	resubscribe(client, testConfig(), subscribed, &once)

	if err := <-subscribed; err != nil {
		t.Fatalf("first subscribed result = %v, want nil", err)
	}
	select {
	case err := <-subscribed:
		t.Fatalf("unexpected second subscribed result: %v", err)
	default:
	}
	if client.subscribeCalls != 2 {
		t.Fatalf("subscribeCalls = %d, want 2", client.subscribeCalls)
	}
}

func TestResubscribeReportsFirstFailure(t *testing.T) {
	wantErr := errors.New("subscribe failed")
	client := &fakeClient{tokens: []mqtt.Token{fakeToken{err: wantErr}}}
	subscribed := make(chan error, 1)
	var once sync.Once

	resubscribe(client, testConfig(), subscribed, &once)

	if got := <-subscribed; !errors.Is(got, wantErr) {
		t.Fatalf("subscribed error = %v, want %v", got, wantErr)
	}
	if client.lastTopic != "africa-west/+/smartmeter/+/telemetry" {
		t.Fatalf("Subscribe topic = %q", client.lastTopic)
	}
	if client.lastQOS != 1 {
		t.Fatalf("Subscribe qos = %d, want 1", client.lastQOS)
	}
}

func TestResubscribeReportsTimeout(t *testing.T) {
	client := &fakeClient{tokens: []mqtt.Token{fakeToken{waitOK: boolPtr(false)}}}
	subscribed := make(chan error, 1)
	var once sync.Once

	resubscribe(client, testConfig(), subscribed, &once)

	if err := <-subscribed; err == nil {
		t.Fatal("subscribed error = nil, want timeout error")
	}
}

func testConfig() config.Config {
	return config.Config{
		MQTTBroker:      "tcp://localhost:1883",
		MQTTTopicFilter: "africa-west/+/smartmeter/+/telemetry",
		MQTTClientID:    "test-client",
		MQTTQOS:         1,
		KafkaBrokers:    []string{"localhost:9092"},
		KafkaTopic:      "telemetry.raw",
		QueueSize:       1,
		WorkerCount:     1,
		ProduceTimeout:  time.Second,
		ShutdownTimeout: time.Second,
	}
}

func boolPtr(v bool) *bool {
	return &v
}

type fakeMessage struct {
	topic     string
	payload   []byte
	duplicate bool
	qos       byte
	retained  bool
	messageID uint16
}

func (m fakeMessage) Duplicate() bool   { return m.duplicate }
func (m fakeMessage) Qos() byte         { return m.qos }
func (m fakeMessage) Retained() bool    { return m.retained }
func (m fakeMessage) Topic() string     { return m.topic }
func (m fakeMessage) MessageID() uint16 { return m.messageID }
func (m fakeMessage) Payload() []byte   { return m.payload }
func (m fakeMessage) Ack()              {}

type fakeToken struct {
	err    error
	waitOK *bool
	done   <-chan struct{}
}

func (t fakeToken) Wait() bool { return true }
func (t fakeToken) WaitTimeout(time.Duration) bool {
	if t.waitOK != nil {
		return *t.waitOK
	}
	return true
}
func (t fakeToken) Done() <-chan struct{} {
	if t.done != nil {
		return t.done
	}
	done := make(chan struct{})
	close(done)
	return done
}
func (t fakeToken) Error() error { return t.err }

type fakeClient struct {
	tokens         []mqtt.Token
	subscribeCalls int
	lastTopic      string
	lastQOS        byte
}

func (c *fakeClient) IsConnected() bool      { return true }
func (c *fakeClient) IsConnectionOpen() bool { return true }
func (c *fakeClient) Connect() mqtt.Token    { return fakeToken{} }
func (c *fakeClient) Disconnect(uint)        {}
func (c *fakeClient) Publish(string, byte, bool, interface{}) mqtt.Token {
	return fakeToken{}
}
func (c *fakeClient) Subscribe(topic string, qos byte, _ mqtt.MessageHandler) mqtt.Token {
	c.subscribeCalls++
	c.lastTopic = topic
	c.lastQOS = qos
	if len(c.tokens) == 0 {
		return fakeToken{}
	}
	token := c.tokens[0]
	c.tokens = c.tokens[1:]
	return token
}
func (c *fakeClient) SubscribeMultiple(map[string]byte, mqtt.MessageHandler) mqtt.Token {
	return fakeToken{}
}
func (c *fakeClient) Unsubscribe(...string) mqtt.Token     { return fakeToken{} }
func (c *fakeClient) AddRoute(string, mqtt.MessageHandler) {}
func (c *fakeClient) OptionsReader() mqtt.ClientOptionsReader {
	return mqtt.ClientOptionsReader{}
}
