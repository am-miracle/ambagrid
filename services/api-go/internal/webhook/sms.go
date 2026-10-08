package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"api-go/internal/domain"
)

type SMSSender struct {
	SiteID    string
	GatewayID string
}

type SMS struct {
	secret  []byte
	senders map[string]SMSSender
}

func NewSMS(secret string, senders map[string]SMSSender) (*SMS, error) {
	if secret == "" || len(senders) == 0 {
		return nil, errors.New("SMS webhook secret and senders are required")
	}
	return &SMS{secret: []byte(secret), senders: senders}, nil
}

func (s *SMS) SignatureHeader() string { return "X-SMS-Signature" }

func (s *SMS) VerifySignature(body []byte, signature string) error {
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return errors.New("invalid SMS signature")
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return errors.New("invalid SMS signature")
	}
	return nil
}

type smsEnvelope struct {
	MessageID  string    `json:"message_id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Body       string    `json:"body"`
	ReceivedAt time.Time `json:"received_at"`
}

func (s *SMS) Parse(body []byte) (domain.CriticalFallbackEvent, domain.SMSReceipt, error) {
	var envelope smsEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.MessageID == "" || envelope.From == "" || envelope.ReceivedAt.IsZero() {
		return domain.CriticalFallbackEvent{}, domain.SMSReceipt{}, errors.New("invalid SMS webhook payload")
	}
	event, err := parseSMSMessage(envelope.Body)
	if err != nil {
		return domain.CriticalFallbackEvent{}, domain.SMSReceipt{}, err
	}
	identity, ok := s.senders[envelope.From]
	if !ok || identity.SiteID != event.SiteID || identity.GatewayID == "" {
		return domain.CriticalFallbackEvent{}, domain.SMSReceipt{}, errors.New("SMS sender does not match the claimed site")
	}
	return event, domain.SMSReceipt{
		ProviderMessageID: envelope.MessageID, Sender: envelope.From, Recipient: envelope.To,
		ReceivedAt: envelope.ReceivedAt.UTC(), GatewayID: identity.GatewayID,
	}, nil
}

func parseSMSMessage(message string) (domain.CriticalFallbackEvent, error) {
	if len(message) == 0 || gsmSeptets(message) > 160 {
		return domain.CriticalFallbackEvent{}, errors.New("SMS must contain one segment")
	}
	parts := strings.Split(message, "|")
	if len(parts) < 7 || parts[0] != "AMBAGRID" {
		return domain.CriticalFallbackEvent{}, errors.New("invalid SMS envelope")
	}
	values := make(map[string]string, len(parts)-1)
	keys := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		if !ok || key == "" || value == "" {
			return domain.CriticalFallbackEvent{}, errors.New("invalid SMS field")
		}
		if _, exists := values[key]; exists {
			return domain.CriticalFallbackEvent{}, fmt.Errorf("duplicate SMS field %q", key)
		}
		values[key] = value
		keys = append(keys, key)
	}
	if values["v"] != "1" {
		return domain.CriticalFallbackEvent{}, errors.New("unsupported SMS version")
	}
	for key := range values {
		switch key {
		case "v", "site", "asset", "seq", "code", "temp", "ts":
		default:
			return domain.CriticalFallbackEvent{}, fmt.Errorf("unknown SMS field %q", key)
		}
	}
	sequence, err := strconv.ParseUint(values["seq"], 10, 64)
	if err != nil || sequence == 0 {
		return domain.CriticalFallbackEvent{}, errors.New("invalid SMS sequence")
	}
	epoch, err := strconv.ParseInt(values["ts"], 10, 64)
	if err != nil || epoch <= 0 {
		return domain.CriticalFallbackEvent{}, errors.New("invalid SMS timestamp")
	}
	event := domain.CriticalFallbackEvent{SiteID: values["site"], AssetID: values["asset"], Sequence: sequence, Code: values["code"], OccurredAt: time.Unix(epoch, 0).UTC()}
	if !validSMSToken(event.SiteID) || !validSMSToken(event.AssetID) || !validCriticalCode(event.Code) {
		return domain.CriticalFallbackEvent{}, errors.New("invalid SMS identity or code")
	}
	if raw := values["temp"]; raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return domain.CriticalFallbackEvent{}, errors.New("invalid SMS temperature")
		}
		event.TemperatureC = &value
	}
	wantKeys := []string{"v", "site", "asset", "seq", "code", "ts"}
	if event.Code == "BATTERY_OVERHEAT" {
		if event.TemperatureC == nil {
			return domain.CriticalFallbackEvent{}, errors.New("battery overheat SMS requires temperature")
		}
		wantKeys = []string{"v", "site", "asset", "seq", "code", "temp", "ts"}
	} else if event.TemperatureC != nil {
		return domain.CriticalFallbackEvent{}, errors.New("temperature is only valid for battery overheat")
	}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		return domain.CriticalFallbackEvent{}, errors.New("SMS fields are missing or out of order")
	}
	if (event.Code == "SITE_OUTAGE") != (event.AssetID == "site") {
		return domain.CriticalFallbackEvent{}, errors.New("site outage must use the reserved site asset")
	}
	return event, nil
}

func validSMSToken(value string) bool {
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return value != ""
}

func gsmSeptets(value string) int {
	count := 0
	for _, r := range value {
		count++
		if strings.ContainsRune("^{}\\[~]|€", r) {
			count++
		}
	}
	return count
}

func validCriticalCode(code string) bool {
	switch code {
	case "BATTERY_OVERHEAT", "INVERTER_FAILURE", "TAMPER_DETECTED", "SITE_OUTAGE":
		return true
	default:
		return false
	}
}
