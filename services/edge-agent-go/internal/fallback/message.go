package fallback

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Code string

const (
	BatteryOverheat Code = "BATTERY_OVERHEAT"
	InverterFailure Code = "INVERTER_FAILURE"
	TamperDetected  Code = "TAMPER_DETECTED"
	SiteOutage      Code = "SITE_OUTAGE"
)

func (c Code) Valid() bool {
	switch c {
	case BatteryOverheat, InverterFailure, TamperDetected, SiteOutage:
		return true
	default:
		return false
	}
}

type Event struct {
	SiteID       string
	AssetID      string
	Sequence     uint64
	Code         Code
	TemperatureC *float64
	OccurredAt   time.Time
}

func (e Event) EventKey() string {
	return fmt.Sprintf("edge:%s:%d", e.SiteID, e.Sequence)
}

func Format(event Event) (string, error) {
	if !event.Code.Valid() {
		return "", fmt.Errorf("unknown critical fallback code %q", event.Code)
	}
	if invalidToken(event.SiteID) || invalidToken(event.AssetID) {
		return "", errors.New("site and asset IDs must be non-empty GSM-7 tokens")
	}
	if event.Sequence == 0 || event.OccurredAt.IsZero() {
		return "", errors.New("sequence and occurrence time are required")
	}
	if event.Code == BatteryOverheat && event.TemperatureC == nil {
		return "", errors.New("battery overheat requires temperature")
	}
	if event.TemperatureC != nil && (math.IsNaN(*event.TemperatureC) || math.IsInf(*event.TemperatureC, 0)) {
		return "", errors.New("temperature must be finite")
	}
	if event.Code != BatteryOverheat && event.TemperatureC != nil {
		return "", errors.New("temperature is only valid for battery overheat")
	}
	if (event.Code == SiteOutage) != (event.AssetID == "site") {
		return "", errors.New("site outage must use the reserved site asset")
	}

	parts := []string{
		"AMBAGRID", "v=1", "site=" + event.SiteID, "asset=" + event.AssetID,
		"seq=" + strconv.FormatUint(event.Sequence, 10), "code=" + string(event.Code),
	}
	if event.TemperatureC != nil {
		parts = append(parts, "temp="+strconv.FormatFloat(*event.TemperatureC, 'f', 1, 64))
	}
	parts = append(parts, "ts="+strconv.FormatInt(event.OccurredAt.UTC().Unix(), 10))
	message := strings.Join(parts, "|")
	if septets := gsmSeptets(message); septets > 160 {
		return "", fmt.Errorf("fallback message is %d GSM-7 septets; maximum is 160", septets)
	}
	return message, nil
}

func invalidToken(value string) bool {
	if value == "" || strings.ContainsAny(value, "|=") {
		return true
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return true
		}
	}
	return false
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
