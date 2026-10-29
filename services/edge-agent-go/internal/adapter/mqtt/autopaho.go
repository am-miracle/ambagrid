package mqtt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

const qosAtLeastOnce = 1

// tlsSchemes are the autopaho URL schemes that connect over TLS.
var tlsSchemes = map[string]bool{"ssl": true, "tls": true, "mqtts": true, "mqtt+ssl": true, "tcps": true, "wss": true}

type AutopahoConfig struct {
	// AllowInsecure permits plain TCP and anonymous access, for isolated development only.
	AllowInsecure bool
	Broker        string
	ClientID      string
	Username      string
	Password      string
	// CAFile is the certificate authority trusted for the broker's TLS certificate.
	CAFile            string
	ConnectTimeout    time.Duration
	ConnectRetryDelay time.Duration
}

// AutopahoTransport is a Transport over an MQTT v5 connection that autopaho
// keeps alive and reconnects.
type AutopahoTransport struct {
	cm         *autopaho.ConnectionManager
	cancel     context.CancelFunc
	life       context.Context
	retryDelay time.Duration
	// up is true only once a connection exists and its subscriptions are
	// restored, so no command is sent before its reply can be received.
	up bool
	// generation changes on every connect and disconnect, so a slow restore
	// cannot mark a connection up after it has already dropped.
	generation    uint64
	restoreCancel context.CancelFunc

	mu   sync.Mutex
	subs map[string]func(Message)
}

func DialAutopaho(ctx context.Context, cfg AutopahoConfig) (*AutopahoTransport, error) {
	broker, err := url.Parse(cfg.Broker)
	if err != nil || broker.Host == "" || broker.User != nil {
		return nil, fmt.Errorf("invalid MQTT broker URL")
	}
	if !cfg.AllowInsecure && (!tlsSchemes[broker.Scheme] || cfg.Username == "" || cfg.Password == "" || cfg.CAFile == "") {
		return nil, fmt.Errorf("command broker requires TLS, credentials and a trusted CA")
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	if cfg.ConnectRetryDelay <= 0 {
		cfg.ConnectRetryDelay = 10 * time.Second
	}
	var tlsConfig *tls.Config
	if cfg.CAFile != "" {
		if tlsConfig, err = tlsFromCA(cfg.CAFile); err != nil {
			return nil, err
		}
	}
	life, cancel := context.WithCancel(context.Background())
	t := &AutopahoTransport{subs: make(map[string]func(Message)), life: life, cancel: cancel, retryDelay: cfg.ConnectRetryDelay}
	cm, err := autopaho.NewConnection(life, autopaho.ClientConfig{
		ServerUrls:      []*url.URL{broker},
		TlsCfg:          tlsConfig,
		KeepAlive:       30,
		ConnectTimeout:  cfg.ConnectTimeout,
		ConnectUsername: cfg.Username,
		ConnectPassword: []byte(cfg.Password),
		// No queue: a publish while disconnected fails instead of being sent,
		// possibly stale, after the connection returns.
		CleanStartOnInitialConnection: true,
		ReconnectBackoff:              func(int) time.Duration { return cfg.ConnectRetryDelay },
		OnConnectionUp:                t.connectionUp,
		OnConnectionDown:              t.connectionDown,
		OnConnectError:                func(err error) { slog.Warn("connect to meter command broker", "error", err) },
		ClientConfig: paho.ClientConfig{
			ClientID:          cfg.ClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){t.received},
		},
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start MQTT connection: %w", err)
	}
	t.cm = cm
	if err := cm.AwaitConnection(ctx); err != nil {
		t.Close()
		return nil, fmt.Errorf("connect to MQTT broker %s: %w", broker.Redacted(), err)
	}
	return t, nil
}

// Connected reports whether commands can be sent and replies received.
func (t *AutopahoTransport) Connected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.up
}

func (t *AutopahoTransport) Publish(ctx context.Context, msg Message, beforeSend func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !t.Connected() {
		return autopaho.ConnectionDownError
	}
	if beforeSend != nil {
		if err := beforeSend(); err != nil {
			return err
		}
	}
	publish := &paho.Publish{QoS: qosAtLeastOnce, Topic: msg.Topic, Payload: msg.Payload}
	if msg.ResponseTopic != "" || len(msg.CorrelationData) > 0 {
		publish.Properties = &paho.PublishProperties{ResponseTopic: msg.ResponseTopic, CorrelationData: msg.CorrelationData}
	}
	response, err := t.cm.Publish(ctx, publish)
	if err != nil {
		return err
	}
	if response != nil && response.ReasonCode >= 0x80 {
		return fmt.Errorf("broker refused publish to %s: reason code %#x", msg.Topic, response.ReasonCode)
	}
	return nil
}

func (t *AutopahoTransport) Subscribe(ctx context.Context, filter string, deliver func(Message)) error {
	t.mu.Lock()
	t.subs[filter] = deliver
	t.mu.Unlock()
	return subscribe(ctx, t.cm, filter)
}

func (t *AutopahoTransport) Close() {
	t.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if t.cm != nil {
		_ = t.cm.Disconnect(ctx)
	}
}

// connectionUp must not block autopaho, so subscriptions are restored in the
// background; the session does not outlive the connection.
func (t *AutopahoTransport) connectionUp(cm *autopaho.ConnectionManager, _ *paho.Connack) {
	t.mu.Lock()
	t.generation++
	generation := t.generation
	t.up = false
	if t.restoreCancel != nil {
		t.restoreCancel()
	}
	ctx, cancel := context.WithCancel(t.life)
	t.restoreCancel = cancel
	filters := make([]string, 0, len(t.subs))
	for filter := range t.subs {
		filters = append(filters, filter)
	}
	t.mu.Unlock()
	go t.restoreSubscriptions(ctx, cm, filters, generation)
}

func (t *AutopahoTransport) connectionDown() bool {
	t.mu.Lock()
	t.generation++
	t.up = false
	if t.restoreCancel != nil {
		t.restoreCancel()
	}
	t.mu.Unlock()
	slog.Warn("meter command broker connection lost")
	return true
}

type subscriptionClient interface {
	Subscribe(context.Context, *paho.Subscribe) (*paho.Suback, error)
}

func (t *AutopahoTransport) restoreSubscriptions(ctx context.Context, client subscriptionClient, filters []string, generation uint64) {
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		var err error
		for _, filter := range filters {
			if err = subscribe(attempt, client, filter); err != nil {
				slog.Warn("restore meter reply subscription", "filter", filter, "error", err)
				break
			}
		}
		cancel()
		if err == nil {
			t.mu.Lock()
			// Readiness and generation share a lock with disconnect, so a
			// stale restore cannot turn readiness back on after a drop.
			if ctx.Err() == nil && t.generation == generation {
				t.up = true
			}
			t.mu.Unlock()
			return
		}
		timer := time.NewTimer(t.retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (t *AutopahoTransport) received(pr paho.PublishReceived) (bool, error) {
	packet := pr.Packet
	msg := Message{Topic: packet.Topic, Payload: packet.Payload}
	if packet.Properties != nil {
		msg.ResponseTopic, msg.CorrelationData = packet.Properties.ResponseTopic, packet.Properties.CorrelationData
	}
	t.mu.Lock()
	var handlers []func(Message)
	for filter, deliver := range t.subs {
		if matches(filter, packet.Topic) {
			handlers = append(handlers, deliver)
		}
	}
	t.mu.Unlock()
	for _, deliver := range handlers {
		deliver(msg)
	}
	return len(handlers) > 0, nil
}

func subscribe(ctx context.Context, cm subscriptionClient, filter string) error {
	ack, err := cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: filter, QoS: qosAtLeastOnce}}})
	if err != nil {
		return fmt.Errorf("subscribe to %s: %w", filter, err)
	}
	for _, code := range ack.Reasons {
		if code >= 0x80 {
			return fmt.Errorf("broker refused subscription to %s: reason code %#x", filter, code)
		}
	}
	return nil
}

func tlsFromCA(path string) (*tls.Config, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read MQTT CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("MQTT CA file contains no certificates")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
