package controller

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
	"api-go/internal/services"
)

const maxEdgeReportBytes = 4 << 10

type EdgeCommandService interface {
	Poll(ctx context.Context, siteID string, limit int) ([]domain.EdgeCommand, error)
	Report(ctx context.Context, siteID string, report domain.CommandReport) (domain.MeterCommand, bool, error)
	ListReconciliation(context.Context, string, int, string) (page.Page[domain.CommandReconciliation], error)
}

// SiteKeys authenticates edge agents by the bearer key issued to their site.
type SiteKeys struct {
	keys map[string]string
}

// NewSiteKeys takes a map from API key to site ID.
func NewSiteKeys(keySites map[string]string) SiteKeys {
	return SiteKeys{keys: keySites}
}

// siteFor compares every key in constant time so timing does not reveal
// which key prefix matched.
func (k SiteKeys) siteFor(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return "", false
	}
	presented := []byte(strings.TrimSpace(auth[len(prefix):]))
	site, found := "", false
	for key, siteID := range k.keys {
		if subtle.ConstantTimeCompare(presented, []byte(key)) == 1 {
			site, found = siteID, true
		}
	}
	return site, found
}

type edgeCommandDTO struct {
	CommandID   string    `json:"command_id"`
	MeterID     string    `json:"meter_id"`
	DeviceID    string    `json:"device_id"`
	CommandType string    `json:"command_type"`
	Status      string    `json:"status"`
	RequestedAt time.Time `json:"requested_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	ExpiresInMS int64     `json:"expires_in_ms"`
	CreditKWh   *float64  `json:"credit_kwh,omitempty"`
}

type edgeCommandReportBody struct {
	Status        string    `json:"status"`
	OccurredAt    time.Time `json:"occurred_at"`
	FailureReason string    `json:"failure_reason,omitempty"`
}

type edgeCommandReportDTO struct {
	CommandID              string `json:"command_id"`
	Status                 string `json:"status"`
	Changed                bool   `json:"changed"`
	ReconciliationRequired bool   `json:"reconciliation_required,omitempty"`
}

func (a API) authenticateSite(w http.ResponseWriter, r *http.Request) (string, bool) {
	siteID, ok := a.EdgeAuth.siteFor(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErrorBody(w, a.logger(), http.StatusUnauthorized, codeUnauthenticated, "missing or unknown site API key", RequestIDFrom(r.Context()))
	}
	return siteID, ok
}

func (a API) handlePollEdgeCommands(w http.ResponseWriter, r *http.Request) {
	siteID, ok := a.authenticateSite(w, r)
	if !ok {
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	commands, err := a.EdgeCommands.Poll(r.Context(), siteID, limit)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	items := make([]edgeCommandDTO, 0, len(commands))
	for _, cmd := range commands {
		items = append(items, edgeCommandDTO{
			CommandID: cmd.CommandID, MeterID: cmd.MeterID, DeviceID: cmd.DeviceID,
			CommandType: string(cmd.CommandType), Status: string(cmd.Status),
			RequestedAt: cmd.RequestedAt.UTC(), ExpiresAt: cmd.ExpiresAt.UTC(),
			ExpiresInMS: cmd.ExpiresIn.Milliseconds(), CreditKWh: cmd.CreditKWh,
		})
	}
	writeObject(w, r, a.logger(), items)
}

func (a API) handleReportEdgeCommand(w http.ResponseWriter, r *http.Request) {
	siteID, ok := a.authenticateSite(w, r)
	if !ok {
		return
	}
	var body edgeCommandReportBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEdgeReportBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, r, a.logger(), errInvalidParameter)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, a.logger(), errInvalidParameter)
		return
	}
	cmd, changed, err := a.EdgeCommands.Report(r.Context(), siteID, domain.CommandReport{
		CommandID:     r.PathValue("command_id"),
		Status:        domain.MeterCommandStatus(body.Status),
		OccurredAt:    body.OccurredAt,
		FailureReason: body.FailureReason,
	})
	if errors.Is(err, services.ErrReportNeedsReconciliation) {
		writeJSON(w, a.logger(), http.StatusAccepted, objectBody[edgeCommandReportDTO]{
			Data:      edgeCommandReportDTO{CommandID: cmd.CommandID, Status: string(cmd.Status), ReconciliationRequired: true},
			RequestID: RequestIDFrom(r.Context()),
		})
		return
	}
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	writeObject(w, r, a.logger(), edgeCommandReportDTO{CommandID: cmd.CommandID, Status: string(cmd.Status), Changed: changed})
}

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
}

func (a API) handleListCommandReconciliation(w http.ResponseWriter, r *http.Request) {
	siteID, ok := a.authenticateSite(w, r)
	if !ok {
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	result, err := a.EdgeCommands.ListReconciliation(r.Context(), siteID, limit, cursorParam(r))
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	writeCollection(w, r, a.logger(), result, func(item domain.CommandReconciliation) commandReconciliationDTO {
		cmd := item.Command
		dto := commandReconciliationDTO{
			CommandID: cmd.CommandID, MeterID: cmd.MeterID, SiteID: item.SiteID,
			CommandType: string(cmd.CommandType), Status: string(cmd.Status), Reason: "overdue_outcome_unknown",
			RequestedAt: cmd.RequestedAt, ExpiresAt: cmd.ExpiresAt, SentAt: cmd.SentAt,
			AgeSeconds: item.Age.Seconds(), OverdueSeconds: item.Overdue.Seconds(),
			LastContactAt: item.Site.LastContactAt, SiteHealth: string(item.Site.HealthStatus),
			LastSMSHeartbeatAt: item.Site.LastSMSHeartbeatAt, LastReportAt: item.LastReportAt,
			ConflictCount: item.ConflictCount, LatestConflictReceivedAt: item.LatestConflictReceivedAt,
		}
		if item.LatestConflict != nil {
			dto.Reason = "conflicting_report"
			dto.LatestConflict = &edgeCommandReportBody{
				Status: string(item.LatestConflict.Status), OccurredAt: item.LatestConflict.OccurredAt,
				FailureReason: item.LatestConflict.FailureReason,
			}
		}
		return dto
	})
}
