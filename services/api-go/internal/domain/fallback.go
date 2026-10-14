package domain

import (
	"fmt"
	"strconv"
	"time"
)

type CriticalFallbackEvent struct {
	SiteID       string
	AssetID      string
	Sequence     uint64
	Code         string
	TemperatureC *float64
	OccurredAt   time.Time
}

func (e CriticalFallbackEvent) EventKey() string {
	return "edge:" + e.SiteID + ":" + strconv.FormatUint(e.Sequence, 10)
}

func CriticalCodeMatchesAsset(code, assetType string) bool {
	switch code {
	case "BATTERY_OVERHEAT":
		return assetType == "battery_bms"
	case "INVERTER_FAILURE":
		return assetType == "solar_inverter"
	default:
		return true
	}
}

func FallbackAlertDetails(event CriticalFallbackEvent) (kind string, reason string) {
	switch event.Code {
	case "BATTERY_OVERHEAT":
		return "battery_overheat", fmt.Sprintf("Battery temperature %.1f C exceeded the critical limit", *event.TemperatureC)
	case "INVERTER_FAILURE":
		return "inverter_failure", "Inverter reported a critical failure"
	case "TAMPER_DETECTED":
		return "tamper_detected", "Asset tamper alarm triggered"
	default:
		return "site_outage", "Full-site outage detected"
	}
}

type StoredFallbackEvent struct {
	AlertID  string
	SiteID   string
	AssetID  string
	Code     string
	Sequence uint64
}

type CreateAlertCommand struct {
	AssetID       string
	SiteID        string
	Kind          string
	Reason        string
	OccurredAt    time.Time
	SourceEventID string
}

type InsertFallbackEventCommand struct {
	EventKey string
	SiteID   string
	Sequence uint64
	AssetID  string
	Code     string
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
