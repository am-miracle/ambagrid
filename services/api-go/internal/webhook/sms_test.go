package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type gsmSeptetFixture struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Repeat  int    `json:"repeat"`
	Septets int    `json:"septets"`
}

func TestSMSParsesAuthenticatedCriticalMessage(t *testing.T) {
	provider, err := NewSMS("secret", map[string]SMSSender{"+2348000000000": {SiteID: "ng-kaji-01", GatewayID: "gateway-01"}})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"message_id":"provider-1","from":"+2348000000000","to":"1234","body":"AMBAGRID|v=1|site=ng-kaji-01|asset=batt-01|seq=18422|code=BATTERY_OVERHEAT|temp=68.2|ts=1730000000","received_at":"2024-10-27T03:33:20Z"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	if err := provider.VerifySignature(body, hex.EncodeToString(mac.Sum(nil))); err != nil {
		t.Fatal(err)
	}
	inbound, err := provider.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if inbound.Heartbeat != nil || inbound.Critical == nil {
		t.Fatalf("inbound = %+v, want a critical event", inbound)
	}
	if inbound.Critical.EventKey() != "edge:ng-kaji-01:18422" || inbound.Receipt.GatewayID != "gateway-01" {
		t.Fatalf("event=%+v receipt=%+v", inbound.Critical, inbound.Receipt)
	}
}

type heartbeatFixture struct {
	HeartbeatKeyPrefix string `json:"heartbeat_key_prefix"`
	Message            string `json:"message"`
	Heartbeat          struct {
		SiteID     string `json:"site_id"`
		GatewayID  string `json:"gateway_id"`
		Sequence   uint64 `json:"sequence"`
		SentAtUnix int64  `json:"sent_at_unix"`
	} `json:"heartbeat"`
	HeartbeatKey string `json:"heartbeat_key"`
}

func loadHeartbeatFixture(t *testing.T) heartbeatFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "proto", "fixtures", "sms_heartbeat_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture heartbeatFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func heartbeatBody(t *testing.T, from, message string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"message_id": "provider-hb-1", "from": from, "to": "1234",
		"body": message, "received_at": "2024-10-27T03:40:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSMSParsesHeartbeatFromSharedContract(t *testing.T) {
	fixture := loadHeartbeatFixture(t)
	provider, err := NewSMS("secret", map[string]SMSSender{"+2348000000000": {SiteID: "ng-kaji-01", GatewayID: "gateway-01"}})
	if err != nil {
		t.Fatal(err)
	}
	inbound, err := provider.Parse(heartbeatBody(t, "+2348000000000", fixture.Message))
	if err != nil {
		t.Fatal(err)
	}
	if inbound.Critical != nil || inbound.Heartbeat == nil {
		t.Fatalf("inbound = %+v, want a heartbeat", inbound)
	}
	got := *inbound.Heartbeat
	if got.SiteID != fixture.Heartbeat.SiteID || got.GatewayID != fixture.Heartbeat.GatewayID ||
		got.Sequence != fixture.Heartbeat.Sequence || got.SentAt.Unix() != fixture.Heartbeat.SentAtUnix {
		t.Fatalf("heartbeat = %+v, fixture = %+v", got, fixture.Heartbeat)
	}
	if string(got.Key()) != fixture.HeartbeatKey || !strings.HasPrefix(fixture.HeartbeatKey, fixture.HeartbeatKeyPrefix) {
		t.Fatalf("heartbeat key = %q, fixture = %q", got.Key(), fixture.HeartbeatKey)
	}
}

func TestSMSRejectsHeartbeatFromAnotherGateway(t *testing.T) {
	fixture := loadHeartbeatFixture(t)
	provider, err := NewSMS("secret", map[string]SMSSender{
		"+2348000000000": {SiteID: "ng-kaji-01", GatewayID: "gateway-02"},
		"+2348000000001": {SiteID: "ng-other-01", GatewayID: "gateway-01"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"+2348000000000", "+2348000000001", "+2349999999999"} {
		if _, err := provider.Parse(heartbeatBody(t, from, fixture.Message)); err == nil {
			t.Fatalf("accepted heartbeat from %s", from)
		}
	}
}

func TestSMSRejectsMalformedHeartbeats(t *testing.T) {
	for name, message := range map[string]string{
		"unknown kind":   "AMBAGRID|v=1|kind=PING|site=ng-kaji-01|gw=gateway-01|seq=42|ts=1730000000",
		"zero sequence":  "AMBAGRID|v=1|kind=HB|site=ng-kaji-01|gw=gateway-01|seq=0|ts=1730000000",
		"reordered":      "AMBAGRID|v=1|kind=HB|gw=gateway-01|site=ng-kaji-01|seq=42|ts=1730000000",
		"critical field": "AMBAGRID|v=1|kind=HB|site=ng-kaji-01|gw=gateway-01|seq=42|code=SITE_OUTAGE|ts=1730000000",
		"bad gateway":    "AMBAGRID|v=1|kind=HB|site=ng-kaji-01|gw=gate.way|seq=42|ts=1730000000",
	} {
		t.Run(name, func(t *testing.T) {
			values, keys, err := splitSMSMessage(message)
			if err == nil {
				_, err = parseHeartbeat(values, keys)
			}
			if err == nil {
				t.Fatalf("accepted %q", message)
			}
		})
	}
}

func TestSMSRejectsNonFiniteAndMultipartMessages(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		message := "AMBAGRID|v=1|site=site-01|asset=batt-01|seq=1|code=BATTERY_OVERHEAT|temp=" + value + "|ts=1730000000"
		if _, err := parseSMSMessage(message); err == nil {
			t.Fatalf("accepted temperature %s", value)
		}
	}
	message := "AMBAGRID|v=1|site=" + strings.Repeat("a", 110) + "|asset=inv-01|seq=1|code=INVERTER_FAILURE|ts=1730000000"
	if _, err := parseSMSMessage(message); err == nil {
		t.Fatal("accepted multipart message")
	}
}

func TestGSMSeptetsConformsToSMSV1Contract(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "..", "proto", "fixtures", "sms_v1_septets.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read GSM-7 contract fixture: %v", err)
	}

	var fixtures []gsmSeptetFixture
	if err := json.Unmarshal(contents, &fixtures); err != nil {
		t.Fatalf("decode GSM-7 contract fixture: %v", err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			value := strings.Repeat(fixture.Value, fixture.Repeat)
			if got := gsmSeptets(value); got != fixture.Septets {
				t.Fatalf("gsmSeptets(%q) = %d, want %d", value, got, fixture.Septets)
			}
		})
	}
}
