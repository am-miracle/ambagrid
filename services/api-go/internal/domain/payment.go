package domain

import (
	"fmt"
	"strings"
	"time"
)

type PaymentStatus string

const (
	PaymentStatusPending   PaymentStatus = "pending"
	PaymentStatusConfirmed PaymentStatus = "confirmed"
	PaymentStatusFailed    PaymentStatus = "failed"
	PaymentStatusReversed  PaymentStatus = "reversed"
)

type MeterCommandType string

const (
	CommandCreditMeter        MeterCommandType = "credit_meter"
	CommandDisconnectMeter    MeterCommandType = "disconnect_meter"
	CommandReconnectMeter     MeterCommandType = "reconnect_meter"
	CommandRequestMeterStatus MeterCommandType = "request_meter_status"
)

type MeterCommandStatus string

const (
	CommandStatusRequested    MeterCommandStatus = "requested"
	CommandStatusQueued       MeterCommandStatus = "queued"
	CommandStatusSent         MeterCommandStatus = "sent"
	CommandStatusAcknowledged MeterCommandStatus = "acknowledged"
	CommandStatusFailed       MeterCommandStatus = "failed"
	CommandStatusExpired      MeterCommandStatus = "expired"
)

type CreditSourceType string

const (
	CreditSourcePayment            CreditSourceType = "payment"
	CreditSourceAdjustment         CreditSourceType = "adjustment"
	CreditSourcePromotion          CreditSourceType = "promotion"
	CreditSourceOperatorCorrection CreditSourceType = "operator_correction"
	CreditSourceEmergencyCredit    CreditSourceType = "emergency_credit"
)

type Payment struct {
	PaymentID         string
	Provider          string
	ExternalReference string
	CustomerID        string
	AmountMinorUnits  int64
	Currency          string
	Status            PaymentStatus
	ConfirmedAt       *time.Time
	CreatedAt         time.Time
}

type EnergyCredit struct {
	CreditID             string
	SiteID               string
	AssignmentID         string
	PaymentID            *string
	TariffPlanID         *string
	SourceType           CreditSourceType
	SourceID             string
	KWhGranted           float64
	MoneyValueMinorUnits *int64
	CreatedAt            time.Time
}

type CreditBalance struct {
	AssignmentID                  string
	RemainingKWh                  float64
	RemainingMoneyValueMinorUnits int64
	UpdatedAt                     time.Time
}

type MeterCommand struct {
	CommandID      string
	MeterID        string
	CommandType    MeterCommandType
	Status         MeterCommandStatus
	RequestedBy    string
	Reason         string
	RequestedAt    time.Time
	SentAt         *time.Time
	AcknowledgedAt *time.Time
	FailureReason  *string
}

type AuditEvent struct {
	AuditEventID string
	SiteID       string
	ActorID      string
	Action       string
	SubjectType  string
	SubjectID    string
	OccurredAt   time.Time
	Metadata     map[string]any
}

type Tariff struct {
	TariffPlanID       string
	PricePerKWh        float64
	Currency           string
	MinorUnitsPerMajor int
}

type MeterAssignment struct {
	AssignmentID string
	SiteID       string
	MeterID      string
	RelayClosed  bool
}

type CustomerSummary struct {
	CustomerID                    string
	AssignmentID                  string
	RemainingKWh                  float64
	RemainingMoneyValueMinorUnits int64
	TotalPayments                 int64
	TotalKWhPurchased             float64
	LastPaymentAt                 *time.Time
	UpdatedAt                     time.Time
}

type ApplyPaymentCommand struct {
	Provider          string
	ExternalReference string
	CustomerID        string
	AmountMinorUnits  int64
	Currency          string
	Status            PaymentStatus
	ConfirmedAt       time.Time
}

type ApplyPaymentResult struct {
	Payment      Payment
	Credit       EnergyCredit
	Balance      CreditBalance
	MeterCommand *MeterCommand
}

func ValidateCustomerID(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%w: customer_id must not be empty", ErrInvalidID)
	}
	return trimmed, nil
}
