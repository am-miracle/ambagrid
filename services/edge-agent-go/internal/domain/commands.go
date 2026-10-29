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

type CommandReport struct {
	ID            int64
	CommandID     string
	Status        CommandReportStatus
	OccurredAt    time.Time
	FailureReason string
	Attempts      int
}
