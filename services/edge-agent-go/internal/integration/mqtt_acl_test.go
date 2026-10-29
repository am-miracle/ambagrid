//go:build integration

package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
)

// Run against the production ACL/config with disposable test credentials.
func TestCommandBrokerAccessControl(t *testing.T) {
	broker := os.Getenv("MQTT_ACL_TEST_BROKER")
	if broker == "" {
		t.Skip("requires commissioned ACL test broker; see deploy/mqtt/test-acl.sh")
	}
	address, err := url.Parse(broker)
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(os.Getenv("MQTT_ACL_TEST_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("test CA invalid")
	}
	received := make(chan string, 16)
	connect := func(user string, allowed bool) *paho.Client {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, err := (&tls.Dialer{Config: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", address.Host)
		if err != nil {
			t.Fatal(err)
		}
		client := paho.NewClient(paho.ClientConfig{
			Conn: conn, ClientID: "acl-test-" + user,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(pr paho.PublishReceived) (bool, error) {
				received <- user + " got " + pr.Packet.Topic + ":" + string(pr.Packet.Payload)
				return true, nil
			}},
		})
		_, err = client.Connect(ctx, &paho.Connect{
			ClientID: "acl-test-" + user, CleanStart: true, KeepAlive: 10,
			Username: user, UsernameFlag: user != "", Password: []byte("acl-test-password"), PasswordFlag: user != "",
		})
		if !allowed {
			if err == nil {
				client.Disconnect(&paho.Disconnect{})
				t.Fatal("anonymous connection accepted")
			}
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Disconnect(&paho.Disconnect{}) })
		return client
	}
	subscribe := func(c *paho.Client, filter string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := c.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: filter, QoS: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	publish := func(c *paho.Client, topic, payload string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// A refused publish still returns a PUBACK, with an error reason code;
		// the protected subscriber not receiving it is what proves the rule.
		_, _ = c.Publish(ctx, &paho.Publish{Topic: topic, QoS: 1, Payload: []byte(payload)})
	}
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("received %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing allowed message %q", want)
		}
	}
	expectNothing := func(what string) {
		t.Helper()
		select {
		case got := <-received:
			t.Fatalf("%s delivered: %s", what, got)
		case <-time.After(200 * time.Millisecond):
		}
	}

	connect("", false)
	gateway := connect("gateway-ng-kaji-01", true)
	device := connect("device-ng-kaji-01-meter-01", true)
	other := connect("gateway-other-site", true)
	telemetry := connect("telemetry-ng-kaji-01", true)

	command := "site/ng-kaji-01/meter/meter-01/cmd"
	subscribe(device, command)
	subscribe(gateway, "site/ng-kaji-01/meter/+/cmd/reply")
	subscribe(other, "site/ng-kaji-01/#")
	subscribe(telemetry, "site/ng-kaji-01/#")

	publish(gateway, command, "allowed-command")
	expect("device-ng-kaji-01-meter-01 got " + command + ":allowed-command")
	publish(device, command+"/reply", "allowed-reply")
	expect("gateway-ng-kaji-01 got " + command + "/reply:allowed-reply")
	expectNothing("cross-site or telemetry read")

	for _, attempt := range []struct {
		name   string
		client *paho.Client
		topic  string
	}{
		{"cross-site command", other, command},
		{"telemetry issuing command", telemetry, command},
		{"telemetry forging device reply", telemetry, command + "/reply"},
		{"device issuing command", device, command},
		{"gateway forging device reply", gateway, command + "/reply"},
		{"device forging other device reply", device, "site/ng-kaji-01/meter/meter-99/cmd/reply"},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			publish(attempt.client, attempt.topic, "forbidden")
			expectNothing("unauthorized publish")
		})
	}
}
