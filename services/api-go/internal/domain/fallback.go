package domain

import (
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
