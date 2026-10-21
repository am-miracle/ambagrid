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

func (s *SMS) Parse(body []byte) (domain.InboundSMS, error) {
	var envelope smsEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.MessageID == "" || envelope.From == "" || envelope.To == "" || envelope.ReceivedAt.IsZero() {
		return domain.InboundSMS{}, errors.New("invalid SMS webhook payload")
	}
	values, keys, err := splitSMSMessage(envelope.Body)
	if err != nil {
		return domain.InboundSMS{}, err
	}
	identity, ok := s.senders[envelope.From]
	if !ok || identity.GatewayID == "" {
		return domain.InboundSMS{}, errors.New("SMS sender does not match the claimed site")
	}
	inbound := domain.InboundSMS{Receipt: domain.SMSReceipt{
		ProviderMessageID: envelope.MessageID, Sender: envelope.From, Recipient: envelope.To,
		ReceivedAt: envelope.ReceivedAt.UTC(), GatewayID: identity.GatewayID,
	}}
	if _, ok := values["kind"]; ok {
		heartbeat, err := parseHeartbeat(values, keys)
		if err != nil {
			return domain.InboundSMS{}, err
		}
		if identity.SiteID != heartbeat.SiteID || identity.GatewayID != heartbeat.GatewayID {
			return domain.InboundSMS{}, errors.New("SMS sender does not match the claimed gateway")
		}
		inbound.Heartbeat = &heartbeat
		return inbound, nil
	}
	event, err := parseCriticalEvent(values, keys)
	if err != nil {
		return domain.InboundSMS{}, err
	}
	if identity.SiteID != event.SiteID {
		return domain.InboundSMS{}, errors.New("SMS sender does not match the claimed site")
	}
	inbound.Critical = &event
	return inbound, nil
}

func parseSMSMessage(message string) (domain.CriticalFallbackEvent, error) {
	values, keys, err := splitSMSMessage(message)
	if err != nil {
		return domain.CriticalFallbackEvent{}, err
	}
	return parseCriticalEvent(values, keys)
}

func splitSMSMessage(message string) (map[string]string, []string, error) {
	if len(message) == 0 || gsmSeptets(message) > 160 {
		return nil, nil, errors.New("SMS must contain one segment")
	}
	parts := strings.Split(message, "|")
	if len(parts) < 7 || parts[0] != "AMBAGRID" {
		return nil, nil, errors.New("invalid SMS envelope")
	}
	values := make(map[string]string, len(parts)-1)
	keys := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		if !ok || key == "" || value == "" {
			return nil, nil, errors.New("invalid SMS field")
		}
		if _, exists := values[key]; exists {
			return nil, nil, fmt.Errorf("duplicate SMS field %q", key)
		}
		values[key] = value
		keys = append(keys, key)
	}
	if values["v"] != "1" {
		return nil, nil, errors.New("unsupported SMS version")
	}
	return values, keys, nil
}

func parseHeartbeat(values map[string]string, keys []string) (domain.GatewayHeartbeat, error) {
	if values["kind"] != "HB" {
		return domain.GatewayHeartbeat{}, errors.New("unknown SMS message kind")
	}
	if strings.Join(keys, ",") != "v,kind,site,gw,seq,ts" {
		return domain.GatewayHeartbeat{}, errors.New("SMS fields are missing or out of order")
	}
	sequence, err := strconv.ParseUint(values["seq"], 10, 64)
	if err != nil || sequence == 0 {
		return domain.GatewayHeartbeat{}, errors.New("invalid SMS sequence")
	}
	epoch, err := strconv.ParseInt(values["ts"], 10, 64)
	if err != nil || epoch <= 0 {
		return domain.GatewayHeartbeat{}, errors.New("invalid SMS timestamp")
	}
	heartbeat := domain.GatewayHeartbeat{
		SiteID: values["site"], GatewayID: values["gw"], Sequence: sequence, SentAt: time.Unix(epoch, 0).UTC(),
	}
	if !validSMSToken(heartbeat.SiteID) || !validSMSToken(heartbeat.GatewayID) {
		return domain.GatewayHeartbeat{}, errors.New("invalid SMS identity")
	}
	return heartbeat, nil
}

func parseCriticalEvent(values map[string]string, keys []string) (domain.CriticalFallbackEvent, error) {
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
	code, err := domain.ParseCriticalCode(values["code"])
	if err != nil {
		return domain.CriticalFallbackEvent{}, errors.New("invalid SMS identity or code")
	}
	event := domain.CriticalFallbackEvent{SiteID: values["site"], AssetID: values["asset"], Sequence: sequence, Code: code, OccurredAt: time.Unix(epoch, 0).UTC()}
	if !validSMSToken(event.SiteID) || !validSMSToken(event.AssetID) {
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
	if event.Code == domain.BatteryOverheatCode {
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
	if (event.Code == domain.SiteOutageCode) != (event.AssetID == "site") {
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

const gsm7ExtensionChars = "^{}\\[~]|€"

func gsmSeptets(value string) int {
	count := 0
	for _, r := range value {
		count++
		if strings.ContainsRune(gsm7ExtensionChars, r) {
			count++
		}
	}
	return count
}
