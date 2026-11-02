package domain

import "time"

// CommandLedgerStats counts commands whose device outcome is not yet known and
// outcome reports still waiting to reach the platform.
type CommandLedgerStats struct {
	Unresolved          int64
	OldestUnresolvedAge time.Duration
	PendingReports      int64
	AbandonedReports    int64
}

type CommandReportStatus string

const (
	ReportSent         CommandReportStatus = "sent"
	ReportAcknowledged CommandReportStatus = "acknowledged"
	ReportFailed       CommandReportStatus = "failed"
	ReportExpired      CommandReportStatus = "expired"
)

type CommandObservation struct {
	RelayClosed     *bool    `json:"relay_closed,omitempty"`
	BalanceKWh      *float64 `json:"balance_kwh,omitempty"`
	TamperDetected  *bool    `json:"tamper_detected,omitempty"`
	FirmwareVersion string   `json:"firmware_version,omitempty"`
	FaultCode       string   `json:"fault_code,omitempty"`
}

type CommandReport struct {
	Observation   *CommandObservation
	ID            int64
	CommandID     string
	Status        CommandReportStatus
	OccurredAt    time.Time
	FailureReason string
	Attempts      int
}
