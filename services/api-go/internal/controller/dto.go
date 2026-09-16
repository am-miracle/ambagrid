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

type siteDTO struct {
	SiteID         string         `json:"site_id"`
	Name           string         `json:"name"`
	Country        *string        `json:"country"`
	Region         *string        `json:"region"`
	GridOperatorID *string        `json:"operator_id"`
	Lat            *float64       `json:"lat"`
	Lng            *float64       `json:"lng"`
	Status         string         `json:"status"`
	AssetCount     int64          `json:"asset_count"`
	LastSeenAt     *time.Time     `json:"last_seen_at"`
	OpenAlerts     alertCountsDTO `json:"open_alerts"`
}

type alertCountsDTO struct {
	Total    int64 `json:"total"`
	Critical int64 `json:"critical"`
	Warning  int64 `json:"warning"`
	Info     int64 `json:"info"`
}

func toSiteDTO(site domain.Site) siteDTO {
	return siteDTO{
		SiteID:         site.SiteID,
		Name:           site.Name,
		Country:        site.Country,
		Region:         site.Region,
		GridOperatorID: site.GridOperatorID,
		Lat:            site.Lat,
		Lng:            site.Lng,
		Status:         site.Status,
		AssetCount:     site.AssetCount,
		LastSeenAt:     site.LastSeenAt,
		OpenAlerts: alertCountsDTO{
			Total:    site.OpenAlerts.Total,
			Critical: site.OpenAlerts.Critical,
			Warning:  site.OpenAlerts.Warning,
			Info:     site.OpenAlerts.Info,
		},
	}
}
