package controller

import (
	"net/http"
	"time"

	"api-go/internal/domain"
)

type commandReconciliationDTO struct {
	CommandID                string                 `json:"command_id"`
	MeterID                  string                 `json:"meter_id"`
	SiteID                   string                 `json:"site_id"`
	CommandType              string                 `json:"command_type"`
	Status                   string                 `json:"status"`
	Reason                   string                 `json:"reconciliation_reason"`
	RequestedAt              time.Time              `json:"requested_at"`
	ExpiresAt                time.Time              `json:"expires_at"`
	SentAt                   *time.Time             `json:"sent_at,omitempty"`
	AgeSeconds               float64                `json:"age_seconds"`
	OverdueSeconds           float64                `json:"overdue_seconds"`
	LastContactAt            *time.Time             `json:"last_contact_at"`
	SiteHealth               string                 `json:"site_health"`
	LastSMSHeartbeatAt       *time.Time             `json:"last_sms_heartbeat_at"`
	LastReportAt             *time.Time             `json:"last_report_at"`
	ConflictCount            int64                  `json:"conflict_count"`
	LatestConflict           *edgeCommandReportBody `json:"latest_conflict,omitempty"`
	LatestConflictReceivedAt *time.Time             `json:"latest_conflict_received_at,omitempty"`
	LegacyExpiredAt          *time.Time             `json:"legacy_expired_at,omitempty"`
}

func (a API) handleListCommandReconciliation(w http.ResponseWriter, r *http.Request) {
	limit, err := limitParam(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	result, err := a.CommandReconciliation.ListReconciliation(r.Context(), optionalParam(r, "site_id"), limit, cursorParam(r))
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	writeCollection(w, r, a.logger(), result, toCommandReconciliationDTO)
}

func toCommandReconciliationDTO(item domain.CommandReconciliation) commandReconciliationDTO {
	cmd := item.Command
	dto := commandReconciliationDTO{
		CommandID: cmd.CommandID, MeterID: cmd.MeterID, SiteID: item.SiteID,
		CommandType: string(cmd.CommandType), Status: string(cmd.Status), Reason: string(item.Reason()),
		RequestedAt: cmd.RequestedAt, ExpiresAt: cmd.ExpiresAt, SentAt: cmd.SentAt,
		AgeSeconds: item.Age.Seconds(), OverdueSeconds: item.Overdue.Seconds(),
		LastContactAt: item.Site.LastContactAt, SiteHealth: string(item.Site.HealthStatus),
		LastSMSHeartbeatAt: item.Site.LastSMSHeartbeatAt, LastReportAt: item.LastReportAt,
		ConflictCount: item.ConflictCount, LatestConflictReceivedAt: item.LatestConflictReceivedAt,
		LegacyExpiredAt: item.LegacyExpiredAt,
	}
	if item.LatestConflict != nil {
		dto.LatestConflict = &edgeCommandReportBody{
			Status: string(item.LatestConflict.Status), OccurredAt: item.LatestConflict.OccurredAt,
			FailureReason: item.LatestConflict.FailureReason,
		}
	}
	return dto
}
