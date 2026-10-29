//go:build integration

package mqtt_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/mqtt"
)

// trackingListener lets a test drop every broker connection itself. mochi
// v2.7.9's Server.Close can deadlock (GetByListener re-takes its RLock while a
// disconnecting client waits for the write lock). After dropping connections,
// remove the listener before Server.Close so it can stop its background loops
// without entering that broken client lookup.
type trackingListener struct {
	net.Listener
	mu     sync.Mutex
	conns  []net.Conn
	closed bool
}

func (l *trackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		if l.closed {
			conn.Close()
		} else {
			l.conns = append(l.conns, conn)
		}
		l.mu.Unlock()
	}
	return conn, err
}

func (l *trackingListener) dropAll() {
	l.Listener.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for _, conn := range l.conns {
		conn.Close()
	}
}

// startBroker runs an in-process MQTT v5 broker on address ("127.0.0.1:0" for
// any port) and returns its address and a function that takes it offline.
func startBroker(t *testing.T, address string) (string, func()) {
	t.Helper()
	inner, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	listener := &trackingListener{Listener: inner}
	server := mochi.New(&mochi.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	if err := server.AddListener(listeners.NewNet("test", listener)); err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			listener.dropAll()
			server.Listeners.Delete("test")
			if err := server.Close(); err != nil {
				t.Errorf("close broker: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return inner.Addr().String(), stop
}

var clients atomic.Int64

func dialTransport(t *testing.T, address, role string) *mqtt.AutopahoTransport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport, err := mqtt.DialAutopaho(ctx, mqtt.AutopahoConfig{
		Broker: "mqtt://" + address, ClientID: fmt.Sprintf("%s-%d", role, clients.Add(1)),
		AllowInsecure: true, ConnectRetryDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.Close)
	return transport
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCommandConformanceOverAutopaho(t *testing.T) {
	runConformance(t, func(t *testing.T) (mqtt.Transport, mqtt.Transport) {
		address, _ := startBroker(t, "127.0.0.1:0")
		return dialTransport(t, address, "edge"), dialTransport(t, address, "meter")
	})
}

func TestAutopahoDoesNotMarkCommandsSentWhileDisconnected(t *testing.T) {
	address, stop := startBroker(t, "127.0.0.1:0")
	edge := dialTransport(t, address, "edge")
	a := newAdapter(t, edge, meterCaps)
	stop()
	waitFor(t, "the connection to drop", func() bool { return !edge.Connected() })

	marked := 0
	ctx := adapter.WithSendHook(context.Background(), func() error { marked++; return nil })
	if _, err := a.Execute(ctx, adapter.Command{ID: "cmd-1", Type: adapter.CommandDisconnect, DeviceID: "meter-01"}); err == nil {
		t.Fatal("command succeeded with the broker down")
	}
	if marked != 0 {
		t.Fatalf("unsent command marked sent %d times", marked)
	}
}

func TestAutopahoRestoresSubscriptionsAfterReconnecting(t *testing.T) {
	address, stop := startBroker(t, "127.0.0.1:0")
	edge, meter := dialTransport(t, address, "edge"), dialTransport(t, address, "meter")
	newMeterDevice(t, meter)
	a := newAdapter(t, edge, meterCaps)

	stop()
	waitFor(t, "both connections to drop", func() bool { return !edge.Connected() && !meter.Connected() })
	startBroker(t, address)
	waitFor(t, "both clients to reconnect", func() bool { return edge.Connected() && meter.Connected() })

	if result, err := execute(a, adapter.Command{ID: "cmd-after-restart", Type: adapter.CommandDisconnect, DeviceID: "meter-01"}); err != nil || result.CommandID != "cmd-after-restart" {
		t.Fatalf("command after reconnect = %+v, %v", result, err)
	}
}

func TestDeviceAdaptersShareAutopahoWithoutOverwritingReplySubscriptions(t *testing.T) {
	address, _ := startBroker(t, "127.0.0.1:0")
	edge, meter := dialTransport(t, address, "edge"), dialTransport(t, address, "meter")
	newMeterDevice(t, meter)
	var devices []adapter.CommandAdapter
	for _, id := range []string{"meter-01", "meter-02"} {
		a, err := mqtt.New(context.Background(), mqtt.Config{DeviceID: id, Topics: testTopics(t), Capabilities: meterCaps, ResponseTimeout: time.Second}, edge)
		if err != nil {
			t.Fatal(err)
		}
		devices = append(devices, a)
	}
	for i, id := range []string{"meter-01", "meter-02"} {
		if _, err := execute(devices[i], adapter.Command{ID: "status-" + id, DeviceID: id, Type: adapter.CommandRequestStatus}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
}
