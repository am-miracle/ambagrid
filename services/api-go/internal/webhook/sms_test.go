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
	event, receipt, err := provider.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if event.EventKey() != "edge:ng-kaji-01:18422" || receipt.GatewayID != "gateway-01" {
		t.Fatalf("event=%+v receipt=%+v", event, receipt)
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
