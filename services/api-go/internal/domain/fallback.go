package domain

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

type CriticalCode string

type EventKey string

const (
	BatteryOverheatCode CriticalCode = "BATTERY_OVERHEAT"
	InverterFailureCode CriticalCode = "INVERTER_FAILURE"
	TamperDetectedCode  CriticalCode = "TAMPER_DETECTED"
	SiteOutageCode      CriticalCode = "SITE_OUTAGE"
)

func ParseCriticalCode(value string) (CriticalCode, error) {
	code := CriticalCode(value)
	if !code.Valid() {
		return "", fmt.Errorf("unknown critical fallback code %q", value)
	}
	return code, nil
}

func (c CriticalCode) Valid() bool {
	switch c {
	case BatteryOverheatCode, InverterFailureCode, TamperDetectedCode, SiteOutageCode:
		return true
	default:
		return false
	}
}

func (c CriticalCode) MatchesAsset(assetType string) bool {
	switch c {
	case BatteryOverheatCode:
		return assetType == "battery_bms"
	case InverterFailureCode:
		return assetType == "solar_inverter"
	case TamperDetectedCode:
		return assetType != ""
	case SiteOutageCode:
		return false
	default:
		return false
	}
}

type CriticalFallbackEvent struct {
	SiteID       string
	AssetID      string
	Sequence     uint64
	Code         CriticalCode
	TemperatureC *float64
	OccurredAt   time.Time
}

func NewEventKey(siteID string, sequence uint64) EventKey {
	return EventKey("edge:" + siteID + ":" + strconv.FormatUint(sequence, 10))
}

func (e CriticalFallbackEvent) EventKey() EventKey {
	return NewEventKey(e.SiteID, e.Sequence)
}

func (e CriticalFallbackEvent) AlertDetails() (kind string, reason string, err error) {
	switch e.Code {
	case BatteryOverheatCode:
		if e.TemperatureC == nil {
			return "", "", errors.New("battery overheat fallback requires temperature")
		}
		return "battery_overheat", fmt.Sprintf("Battery temperature %.1f C exceeded the critical limit", *e.TemperatureC), nil
	case InverterFailureCode:
		return "inverter_failure", "Inverter reported a critical failure", nil
	case TamperDetectedCode:
		return "tamper_detected", "Asset tamper alarm triggered", nil
	case SiteOutageCode:
		return "site_outage", "Full-site outage detected", nil
	default:
		return "", "", fmt.Errorf("unknown critical fallback code %q", e.Code)
	}
}

type StoredFallbackEvent struct {
	AlertID  string
	SiteID   string
	AssetID  string
	Code     CriticalCode
	Sequence uint64
}

func (s *StoredFallbackEvent) Matches(e CriticalFallbackEvent) bool {
	return s.SiteID == e.SiteID && s.AssetID == e.AssetID && s.Code == e.Code && s.Sequence == e.Sequence
}

type CreateAlertCommand struct {
	AssetID       string
	SiteID        string
	Kind          string
	Reason        string
	OccurredAt    time.Time
	SourceEventID EventKey
}

type InsertFallbackEventCommand struct {
	EventKey EventKey
	SiteID   string
	Sequence uint64
	AssetID  string
	Code     CriticalCode
	EventAt  time.Time
	AlertID  string
}

type SMSReceipt struct {
	ProviderMessageID string
	Sender            string
	Recipient         string
	ReceivedAt        time.Time
	GatewayID         string
}

type SMSPricing struct {
	OutboundCostMinor  int64
	InboundCostMinor   int64
	NumberRentalMinor  int64
	MonthlyBudgetMinor int64
	Currency           string
}
