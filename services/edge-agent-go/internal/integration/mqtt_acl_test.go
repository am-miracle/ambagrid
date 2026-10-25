//go:build integration

package integration_test

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// Run against the production ACL/config with disposable test credentials.
func TestCommandBrokerAccessControl(t *testing.T) {
	broker := os.Getenv("MQTT_ACL_TEST_BROKER")
	if broker == "" {
		t.Skip("requires commissioned ACL test broker; see deploy/mqtt/test-acl.sh")
	}
	pem, err := os.ReadFile(os.Getenv("MQTT_ACL_TEST_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("test CA invalid")
	}
	wait := func(token paho.Token) error {
		t.Helper()
		if !token.WaitTimeout(3 * time.Second) {
			t.Fatal("MQTT operation stalled")
		}
		return token.Error()
	}
	connect := func(user string, allowed bool) paho.Client {
		t.Helper()
		opts := paho.NewClientOptions().AddBroker(broker).SetClientID("acl-test-" + user).SetUsername(user).SetPassword("acl-test-password").SetAutoReconnect(false).SetConnectTimeout(time.Second)
		opts.SetTLSConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
		client := paho.NewClient(opts)
		err := wait(client.Connect())
		if !allowed {
			if err == nil {
				client.Disconnect(0)
				t.Fatal("anonymous connection accepted")
			}
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Disconnect(100) })
		return client
	}
	connect("", false)
	gateway := connect("gateway-ng-kaji-01", true)
	device := connect("device-ng-kaji-01-meter-01", true)
	other := connect("gateway-other-site", true)
	deniedReads := make(chan string, 4)
	wait(other.Subscribe("ambagrid/ng-kaji-01/#", 1, func(_ paho.Client, m paho.Message) { deniedReads <- m.Topic() }))
	received := make(chan string, 16)
	handler := func(_ paho.Client, m paho.Message) { received <- m.Topic() + ":" + string(m.Payload()) }
	command := "ambagrid/ng-kaji-01/meter-01/command"
	if err := wait(device.Subscribe(command, 1, handler)); err != nil {
		t.Fatal(err)
	}
	if err := wait(gateway.Subscribe("ambagrid/ng-kaji-01/+/command/reply", 1, handler)); err != nil {
		t.Fatal(err)
	}
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("received %s want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing allowed message %s", want)
		}
	}
	if err := wait(gateway.Publish(command, 1, false, "allowed-command")); err != nil {
		t.Fatal(err)
	}
	expect(command + ":allowed-command")
	if err := wait(device.Publish(command+"/reply", 1, false, "allowed-reply")); err != nil {
		t.Fatal(err)
	}
	expect(command + "/reply:allowed-reply")
	select {
	case topic := <-deniedReads:
		t.Fatalf("cross-site read allowed: %s", topic)
	case <-time.After(200 * time.Millisecond):
	}
	for _, attempt := range []struct {
		name   string
		client paho.Client
		topic  string
	}{
		{"cross-site command", other, command},
		{"device issuing command", device, command},
		{"gateway forging device reply", gateway, command + "/reply"},
		{"device forging other device reply", device, "ambagrid/ng-kaji-01/meter-99/command/reply"},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			// MQTT 3 PUBACK may acknowledge discarded unauthorized writes; assert the
			// protected subscriber never receives them, not just Publish's return code.
			wait(attempt.client.Publish(attempt.topic, 1, false, "forbidden"))
			select {
			case got := <-received:
				t.Fatalf("unauthorized delivery: %s", got)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}
