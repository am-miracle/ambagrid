package domain

import "time"

type (
	CaseState       string
	CaseFinding     string
	CaseDisposition string
)

const (
	CaseInvestigating CaseState = "investigating"
	CaseResolved      CaseState = "resolved"

	FindingUnknown      CaseFinding = "unknown"
	FindingDelivered    CaseFinding = "delivered"
	FindingNotDelivered CaseFinding = "not_delivered"

	DispositionInvestigate CaseDisposition = "investigate"
	DispositionDelivered   CaseDisposition = "delivered"
	DispositionRedeliver   CaseDisposition = "redeliver"
	DispositionRefund      CaseDisposition = "refund"
	DispositionReview      CaseDisposition = "review"
)

// ReconciliationCase records a business decision without altering device history.
type ReconciliationCase struct {
	CommandID            string          `json:"command_id"`
	State                CaseState       `json:"state"`
	NeedsReview          bool            `json:"needs_review"`
	Owner                string          `json:"owner"`
	Finding              CaseFinding     `json:"finding"`
	Disposition          CaseDisposition `json:"disposition"`
	Reason               string          `json:"reason"`
	Evidence             string          `json:"evidence"`
	ResolvedBy           *string         `json:"resolved_by,omitempty"`
	ReplacementCommandID *string         `json:"replacement_command_id,omitempty"`
	RefundReference      *string         `json:"refund_reference,omitempty"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type ReconcileCommand struct {
	ReviewedConflictID    string
	ReviewedConflictCount int64
	CommandID             string
	Actor                 string
	Owner                 string
	Finding               CaseFinding
	Disposition           CaseDisposition
	Reason                string
	Evidence              string
	RefundReference       string
}

// ReconcileTarget is a command locked for a decision; CreditID is nil for
// commands with no funded entitlement.
type ReconcileTarget struct {
	Command  MeterCommand
	CreditID *string
	SiteID   string
}

// CreditSettlement is what an entitlement's delivery attempts already establish.
// UncertainDelivery covers any retained report that a device may have acted.
type CreditSettlement struct {
	Superseded              bool
	Refunded                bool
	Acknowledged            bool
	AcknowledgementConflict bool
	UncertainDelivery       bool
	Closed                  bool
}

// HasDeviceAcknowledgement means a device, not an operator, reported delivery.
func (s CreditSettlement) HasDeviceAcknowledgement() bool {
	return s.Acknowledged || s.AcknowledgementConflict
}

// Finalized means no further decision may move or close this entitlement.
func (s CreditSettlement) Finalized() bool {
	return s.Superseded || s.Refunded || s.Acknowledged || s.Closed
}

// MeterCommandProfile is what a meter was commissioned for; AssignmentID is
// empty for an unassigned meter.
type MeterCommandProfile struct {
	SiteID       string
	AssignmentID string
	RelayControl bool
	StatusRead   bool
	// BalanceExhausted is set only for a platform-balance meter with an assignment.
	BalanceExhausted bool
}

// RefundableCredit is a funded meter credit and the balance it was paid into.
type RefundableCredit struct {
	Credit  CreditValue
	Balance CreditValue
}

// RefundShortfall is a discrepancy to investigate, not customer debt.
type RefundShortfall struct {
	CreditID   string
	CommandID  string
	Reference  string
	Shortfall  CreditValue
	RefundedAt time.Time
}

// OperatorCommandTemplate is a manual command an operator may issue. Credit
// commands are absent because they only come from funded payment delivery.
type OperatorCommandTemplate struct {
	Type          MeterCommandType
	Label         string
	DefaultReason string
	NeedsConfirm  bool
}

var OperatorCommandTemplates = []OperatorCommandTemplate{
	{CommandRequestMeterStatus, "Read meter status", "Investigate meter state", false},
	{CommandDisconnectMeter, "Disconnect meter", "Operator authorized disconnection", true},
	{CommandReconnectMeter, "Reconnect meter", "Operator authorized reconnection", true},
}

type CreateMeterCommand struct {
	RequestID      string
	MeterID        string
	Type           MeterCommandType
	Reason         string
	ConfirmMeterID string
	Actor          string
}

// CommandObservation is device evidence, never an inference from command type.
type CommandObservation struct {
	RelayClosed     *bool    `json:"relay_closed,omitempty"`
	BalanceKWh      *float64 `json:"balance_kwh,omitempty"`
	TamperDetected  *bool    `json:"tamper_detected,omitempty"`
	FirmwareVersion string   `json:"firmware_version,omitempty"`
	FaultCode       string   `json:"fault_code,omitempty"`
}

// RetainedCommandReport is conflicting device evidence with a stable review ID.
type RetainedCommandReport struct {
	ReportID      string
	ConflictCount int64
	Status        MeterCommandStatus
	OccurredAt    time.Time
	ReceivedAt    time.Time
	FailureReason string
	Observation   *CommandObservation
}

type MeterCommandDetail struct {
	LatestConflict *RetainedCommandReport
	Command        MeterCommand
	Observation    *CommandObservation
	ObservedAt     *time.Time
	ReceivedAt     *time.Time
	Case           *ReconciliationCase
}

// ReconciliationBacklog is work waiting on an operator. Oldest ages are zero
// when the matching count is zero.
type ReconciliationBacklog struct {
	OpenCases         int64
	OldestOpenCase    time.Duration
	UnvendedCredits   int64
	OldestUnvendedSTS time.Duration
}
