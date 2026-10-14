// maps domain models to public JSON types.
package controller

import (
	"time"

	"api-go/internal/domain"
	"api-go/internal/services"
)

// separate wire types keep database changes from changing the API by accident.
// nullable readings remain present so clients can distinguish missing data.

type assetDTO struct {
	AssetID             string           `json:"asset_id"`
	SiteID              string           `json:"site_id"`
	AssetType           domain.AssetType `json:"asset_type"`
	InternalTemperature *float32         `json:"internal_temperature"`
	LastSeenAt          time.Time        `json:"last_seen_at"`
	UpdatedAt           time.Time        `json:"updated_at"`

	SmartMeter    *smartMeterStateDTO    `json:"smart_meter,omitempty"`
	BatteryBMS    *batteryBMSStateDTO    `json:"battery_bms,omitempty"`
	SolarInverter *solarInverterStateDTO `json:"solar_inverter,omitempty"`
}

type smartMeterStateDTO struct {
	ReportedHouseholdID *string   `json:"reported_household_id"`
	RelayClosed         *bool     `json:"relay_closed"`
	Voltage             *float32  `json:"voltage"`
	Current             *float32  `json:"current"`
	ActivePower         *float32  `json:"active_power"`
	Frequency           *float32  `json:"frequency"`
	TotalKWh            *float64  `json:"total_kwh"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type batteryBMSStateDTO struct {
	BatterySOCPct *float32  `json:"battery_soc_pct"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type solarInverterStateDTO struct {
	SolarIrradiance *float32  `json:"solar_irradiance"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func toAssetDTO(asset domain.Asset) assetDTO {
	dto := assetDTO{
		AssetID:             asset.AssetID,
		SiteID:              asset.SiteID,
		AssetType:           asset.AssetType,
		InternalTemperature: asset.InternalTemperature,
		LastSeenAt:          asset.LastSeenAt,
		UpdatedAt:           asset.UpdatedAt,
	}

	if state := asset.SmartMeter; state != nil {
		dto.SmartMeter = &smartMeterStateDTO{
			ReportedHouseholdID: state.ReportedHouseholdID,
			RelayClosed:         state.RelayClosed,
			Voltage:             state.Voltage,
			Current:             state.Current,
			ActivePower:         state.ActivePower,
			Frequency:           state.Frequency,
			TotalKWh:            state.TotalKWh,
			UpdatedAt:           state.UpdatedAt,
		}
	}
	if state := asset.BatteryBMS; state != nil {
		dto.BatteryBMS = &batteryBMSStateDTO{
			BatterySOCPct: state.BatterySOCPct,
			UpdatedAt:     state.UpdatedAt,
		}
	}
	if state := asset.SolarInverter; state != nil {
		dto.SolarInverter = &solarInverterStateDTO{
			SolarIrradiance: state.SolarIrradiance,
			UpdatedAt:       state.UpdatedAt,
		}
	}

	return dto
}

type readingSeriesDTO struct {
	AssetID     string                    `json:"asset_id"`
	AssetType   domain.AssetType          `json:"asset_type"`
	From        time.Time                 `json:"from"`
	To          time.Time                 `json:"to"`
	Metric      domain.ReadingMetric      `json:"metric"`
	Interval    domain.ReadingInterval    `json:"interval"`
	Aggregation domain.ReadingAggregation `json:"aggregation"`
	Points      []readingPointDTO         `json:"points"`
}

type readingPointDTO struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

func toReadingSeriesDTO(series domain.ReadingSeries) readingSeriesDTO {
	points := make([]readingPointDTO, 0, len(series.Points))
	for _, point := range series.Points {
		points = append(points, readingPointDTO{Time: point.Time, Value: point.Value})
	}
	return readingSeriesDTO{
		AssetID:     series.AssetID,
		AssetType:   series.AssetType,
		From:        series.From,
		To:          series.To,
		Metric:      series.Metric,
		Interval:    series.Interval,
		Aggregation: series.Aggregation,
		Points:      points,
	}
}

type alertDTO struct {
	AlertID        string             `json:"alert_id"`
	AssetID        string             `json:"asset_id"`
	SiteID         string             `json:"site_id"`
	Kind           string             `json:"kind"`
	Severity       domain.Severity    `json:"severity"`
	Status         domain.AlertStatus `json:"status"`
	Reason         string             `json:"reason"`
	OpenedAt       time.Time          `json:"opened_at"`
	SourceEventID  *string            `json:"source_event_id"`
	ResolvedAt     *time.Time         `json:"resolved_at"`
	ResolutionNote *string            `json:"resolution_note"`
	ResolvedBy     *string            `json:"resolved_by"`
}

func toAlertDTO(alert domain.Alert) alertDTO {
	return alertDTO{
		AlertID:        alert.AlertID,
		AssetID:        alert.AssetID,
		SiteID:         alert.SiteID,
		Kind:           alert.Kind,
		Severity:       alert.Severity,
		Status:         alert.Status,
		Reason:         alert.Reason,
		OpenedAt:       alert.OpenedAt,
		SourceEventID:  alert.SourceEventID,
		ResolvedAt:     alert.ResolvedAt,
		ResolutionNote: alert.ResolutionNote,
		ResolvedBy:     alert.ResolvedBy,
	}
}

// alertDetailDTO includes the history used by the alert detail view.
type alertDetailDTO struct {
	alertDTO
	Resolutions []alertResolutionDTO `json:"resolutions"`
}

func toAlertDetailDTO(detail services.AlertDetail) alertDetailDTO {
	resolutions := make([]alertResolutionDTO, 0, len(detail.Resolutions))
	for _, resolution := range detail.Resolutions {
		resolutions = append(resolutions, toAlertResolutionDTO(resolution))
	}

	return alertDetailDTO{
		alertDTO:    toAlertDTO(detail.Alert),
		Resolutions: resolutions,
	}
}

type alertResolutionDTO struct {
	ResolutionID   string    `json:"resolution_id"`
	ResolvedAt     time.Time `json:"resolved_at"`
	ResolutionNote string    `json:"resolution_note"`
	ResolvedBy     string    `json:"resolved_by"`
	RecordedAt     time.Time `json:"recorded_at"`
}

func toAlertResolutionDTO(resolution domain.AlertResolution) alertResolutionDTO {
	return alertResolutionDTO{
		ResolutionID:   resolution.ResolutionID,
		ResolvedAt:     resolution.ResolvedAt,
		ResolutionNote: resolution.ResolutionNote,
		ResolvedBy:     resolution.ResolvedBy,
		RecordedAt:     resolution.RecordedAt,
	}
}

type paymentDTO struct {
	PaymentID         string               `json:"payment_id"`
	Provider          string               `json:"provider"`
	ExternalReference string               `json:"external_reference"`
	CustomerID        string               `json:"customer_id"`
	AmountMinorUnits  int64                `json:"amount_minor_units"`
	Currency          string               `json:"currency"`
	Status            domain.PaymentStatus `json:"status"`
	ConfirmedAt       *time.Time           `json:"confirmed_at"`
	CreatedAt         time.Time            `json:"created_at"`
}

type energyCreditDTO struct {
	CreditID             string    `json:"credit_id"`
	SiteID               string    `json:"site_id"`
	AssignmentID         string    `json:"assignment_id"`
	PaymentID            *string   `json:"payment_id"`
	TariffPlanID         *string   `json:"tariff_plan_id"`
	SourceType           string    `json:"source_type"`
	SourceID             string    `json:"source_id"`
	KWhGranted           float64   `json:"kwh_granted"`
	MoneyValueMinorUnits *int64    `json:"money_value_minor_units"`
	CreatedAt            time.Time `json:"created_at"`
}

type creditBalanceDTO struct {
	AssignmentID                  string    `json:"assignment_id"`
	RemainingKWh                  float64   `json:"remaining_kwh"`
	RemainingMoneyValueMinorUnits int64     `json:"remaining_money_value_minor_units"`
	UpdatedAt                     time.Time `json:"updated_at"`
}

type meterCommandDTO struct {
	CommandID      string     `json:"command_id"`
	MeterID        string     `json:"meter_id"`
	CommandType    string     `json:"command_type"`
	Status         string     `json:"status"`
	RequestedBy    string     `json:"requested_by"`
	Reason         string     `json:"reason"`
	RequestedAt    time.Time  `json:"requested_at"`
	SentAt         *time.Time `json:"sent_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
	FailureReason  *string    `json:"failure_reason"`
}

type applyPaymentResultDTO struct {
	Payment      paymentDTO       `json:"payment"`
	Credit       energyCreditDTO  `json:"credit"`
	Balance      creditBalanceDTO `json:"balance"`
	MeterCommand *meterCommandDTO `json:"meter_command"`
}

func toApplyPaymentResultDTO(result domain.ApplyPaymentResult) applyPaymentResultDTO {
	dto := applyPaymentResultDTO{
		Payment: paymentDTO{
			PaymentID:         result.Payment.PaymentID,
			Provider:          result.Payment.Provider,
			ExternalReference: result.Payment.ExternalReference,
			CustomerID:        result.Payment.CustomerID,
			AmountMinorUnits:  result.Payment.AmountMinorUnits,
			Currency:          result.Payment.Currency,
			Status:            result.Payment.Status,
			ConfirmedAt:       result.Payment.ConfirmedAt,
			CreatedAt:         result.Payment.CreatedAt,
		},
		Credit: energyCreditDTO{
			CreditID:             result.Credit.CreditID,
			SiteID:               result.Credit.SiteID,
			AssignmentID:         result.Credit.AssignmentID,
			PaymentID:            result.Credit.PaymentID,
			TariffPlanID:         result.Credit.TariffPlanID,
			SourceType:           string(result.Credit.SourceType),
			SourceID:             result.Credit.SourceID,
			KWhGranted:           result.Credit.KWhGranted,
			MoneyValueMinorUnits: result.Credit.MoneyValueMinorUnits,
			CreatedAt:            result.Credit.CreatedAt,
		},
		Balance: creditBalanceDTO{
			AssignmentID:                  result.Balance.AssignmentID,
			RemainingKWh:                  result.Balance.RemainingKWh,
			RemainingMoneyValueMinorUnits: result.Balance.RemainingMoneyValueMinorUnits,
			UpdatedAt:                     result.Balance.UpdatedAt,
		},
	}

	if cmd := result.MeterCommand; cmd != nil {
		dto.MeterCommand = &meterCommandDTO{
			CommandID:      cmd.CommandID,
			MeterID:        cmd.MeterID,
			CommandType:    string(cmd.CommandType),
			Status:         string(cmd.Status),
			RequestedBy:    cmd.RequestedBy,
			Reason:         cmd.Reason,
			RequestedAt:    cmd.RequestedAt,
			SentAt:         cmd.SentAt,
			AcknowledgedAt: cmd.AcknowledgedAt,
			FailureReason:  cmd.FailureReason,
		}
	}

	return dto
}

type customerSummaryDTO struct {
	CustomerID                    string     `json:"customer_id"`
	AssignmentID                  string     `json:"assignment_id"`
	RemainingKWh                  float64    `json:"remaining_kwh"`
	RemainingMoneyValueMinorUnits int64      `json:"remaining_money_value_minor_units"`
	TotalPayments                 int64      `json:"total_payments"`
	TotalKWhPurchased             float64    `json:"total_kwh_purchased"`
	LastPaymentAt                 *time.Time `json:"last_payment_at"`
	UpdatedAt                     time.Time  `json:"updated_at"`
}

func toCustomerSummaryDTO(cs domain.CustomerSummary) customerSummaryDTO {
	return customerSummaryDTO{
		CustomerID:                    cs.CustomerID,
		AssignmentID:                  cs.AssignmentID,
		RemainingKWh:                  cs.RemainingKWh,
		RemainingMoneyValueMinorUnits: cs.RemainingMoneyValueMinorUnits,
		TotalPayments:                 cs.TotalPayments,
		TotalKWhPurchased:             cs.TotalKWhPurchased,
		LastPaymentAt:                 cs.LastPaymentAt,
		UpdatedAt:                     cs.UpdatedAt,
	}
}

type auditEventDTO struct {
	EventID    string         `json:"event_id"`
	EventType  string         `json:"event_type"`
	EntityType string         `json:"entity_type"`
	EntityID   string         `json:"entity_id"`
	CustomerID string         `json:"customer_id"`
	SiteID     string         `json:"site_id"`
	Detail     map[string]any `json:"detail"`
	CreatedAt  time.Time      `json:"created_at"`
}

func toAuditEventDTO(ev domain.AuditEvent) auditEventDTO {
	customerID, _ := ev.Metadata["customer_id"].(string)
	detail := make(map[string]any, len(ev.Metadata))
	for k, v := range ev.Metadata {
		if k != "customer_id" {
			detail[k] = v
		}
	}
	return auditEventDTO{
		EventID:    ev.AuditEventID,
		EventType:  ev.Action,
		EntityType: ev.SubjectType,
		EntityID:   ev.SubjectID,
		CustomerID: customerID,
		SiteID:     ev.SiteID,
		Detail:     detail,
		CreatedAt:  ev.OccurredAt,
	}
}

func toMeterCommandDTO(cmd domain.MeterCommand) meterCommandDTO {
	return meterCommandDTO{
		CommandID:      cmd.CommandID,
		MeterID:        cmd.MeterID,
		CommandType:    string(cmd.CommandType),
		Status:         string(cmd.Status),
		RequestedBy:    cmd.RequestedBy,
		Reason:         cmd.Reason,
		RequestedAt:    cmd.RequestedAt,
		SentAt:         cmd.SentAt,
		AcknowledgedAt: cmd.AcknowledgedAt,
		FailureReason:  cmd.FailureReason,
	}
}

type siteDTO struct {
	SiteID                  string                  `json:"site_id"`
	Name                    string                  `json:"name"`
	Country                 *string                 `json:"country"`
	Region                  *string                 `json:"region"`
	GridOperatorID          *string                 `json:"operator_id"`
	Lat                     *float64                `json:"lat"`
	Lng                     *float64                `json:"lng"`
	Status                  string                  `json:"status"`
	AssetCount              int64                   `json:"asset_count"`
	LastSeenAt              *time.Time              `json:"last_seen_at"`
	HealthStatus            domain.SiteHealthStatus `json:"health_status"`
	LastContactAt           *time.Time              `json:"last_contact_at"`
	LastEventAt             *time.Time              `json:"last_event_timestamp"`
	QueueDepth              int64                   `json:"queue_depth"`
	OldestPendingAt         *time.Time              `json:"oldest_pending_at"`
	OldestPendingAgeSeconds *int64                  `json:"oldest_pending_record_age_seconds"`
	QueueGrowing            bool                    `json:"queue_growing"`
	OpenAlerts              alertCountsDTO          `json:"open_alerts"`
}

type alertCountsDTO struct {
	Total    int64 `json:"total"`
	Critical int64 `json:"critical"`
	Warning  int64 `json:"warning"`
	Info     int64 `json:"info"`
}

func toSiteDTO(site domain.Site) siteDTO {
	var oldestPendingAgeSeconds *int64
	if site.OldestPendingAt != nil {
		seconds := int64(time.Since(*site.OldestPendingAt).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		oldestPendingAgeSeconds = &seconds
	}
	return siteDTO{
		SiteID:                  site.SiteID,
		Name:                    site.Name,
		Country:                 site.Country,
		Region:                  site.Region,
		GridOperatorID:          site.GridOperatorID,
		Lat:                     site.Lat,
		Lng:                     site.Lng,
		Status:                  site.Status,
		AssetCount:              site.AssetCount,
		LastSeenAt:              site.LastSeenAt,
		HealthStatus:            site.HealthStatus,
		LastContactAt:           site.LastContactAt,
		LastEventAt:             site.LastEventAt,
		QueueDepth:              site.QueueDepth,
		OldestPendingAt:         site.OldestPendingAt,
		OldestPendingAgeSeconds: oldestPendingAgeSeconds,
		QueueGrowing:            site.QueueGrowing,
		OpenAlerts: alertCountsDTO{
			Total:    site.OpenAlerts.Total,
			Critical: site.OpenAlerts.Critical,
			Warning:  site.OpenAlerts.Warning,
			Info:     site.OpenAlerts.Info,
		},
	}
}
