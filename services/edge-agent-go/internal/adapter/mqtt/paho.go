package mqtt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const qosAtLeastOnce = 1

type PahoConfig struct {
	Broker   string
	ClientID string
	Username string
	Password string
	// CAFile enables TLS with this certificate authority; empty means plain TCP.
	CAFile         string
	ConnectTimeout time.Duration
}

// PahoTransport is a Transport over an MQTT broker connection.
type PahoTransport struct {
	client paho.Client

	mu            sync.Mutex
	subscriptions map[string]paho.MessageHandler
}

func DialPaho(ctx context.Context, cfg PahoConfig) (*PahoTransport, error) {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	t := &PahoTransport{subscriptions: make(map[string]paho.MessageHandler)}
	opts := paho.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetConnectTimeout(cfg.ConnectTimeout).
		SetAutoReconnect(true).
		// A clean session drops subscriptions on reconnect, so they are restored here.
		SetCleanSession(true).
		// Unordered delivery stops one slow handler from stalling the network loop.
		SetOrderMatters(false).
		SetOnConnectHandler(t.resubscribe).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			slog.Warn("meter command broker connection lost", "error", err)
		})
	if cfg.CAFile != "" {
		tlsConfig, err := tlsFromCA(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsConfig)
	}
	t.client = paho.NewClient(opts)
	if err := wait(ctx, t.client.Connect()); err != nil {
		return nil, fmt.Errorf("connect to MQTT broker %s: %w", cfg.Broker, err)
	}
	return t, nil
}

func (t *PahoTransport) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Do not enqueue a new command during reconnect. A message already handed
	// to Paho can still finish after cancellation; the ledger keeps it unresolved.
	if !t.client.IsConnectionOpen() {
		return errors.New("MQTT command connection is not open")
	}
	return wait(ctx, t.client.Publish(topic, qosAtLeastOnce, false, payload))
}

func (t *PahoTransport) Subscribe(ctx context.Context, filter string, deliver func(topic string, payload []byte)) error {
	handler := func(_ paho.Client, message paho.Message) {
		deliver(message.Topic(), message.Payload())
	}
	t.mu.Lock()
	t.subscriptions[filter] = handler
	t.mu.Unlock()
	return wait(ctx, t.client.Subscribe(filter, qosAtLeastOnce, handler))
}

func (t *PahoTransport) Close() {
	t.client.Disconnect(250)
}

func (t *PahoTransport) resubscribe(client paho.Client) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for filter, handler := range t.subscriptions {
		// The handler must not block paho's connect path, so this does not wait.
		token := client.Subscribe(filter, qosAtLeastOnce, handler)
		go func() {
			if token.Wait() && token.Error() != nil {
				slog.Error("restore meter reply subscription", "filter", filter, "error", token.Error())
			}
		}()
	}
}

func wait(ctx context.Context, token paho.Token) error {
	select {
	case <-token.Done():
		return token.Error()
	case <-ctx.Done():
		return ctx.Err()
	}
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
