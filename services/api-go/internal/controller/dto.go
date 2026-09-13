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
	AssetID             string    `json:"asset_id"`
	SiteID              string    `json:"site_id"`
	AssetType           string    `json:"asset_type"`
	InternalTemperature *float32  `json:"internal_temperature"`
	LastSeenAt          time.Time `json:"last_seen_at"`
	UpdatedAt           time.Time `json:"updated_at"`

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
		AssetType:           string(asset.AssetType),
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

type alertDTO struct {
	AlertID        string     `json:"alert_id"`
	AssetID        string     `json:"asset_id"`
	SiteID         string     `json:"site_id"`
	Kind           string     `json:"kind"`
	Severity       string     `json:"severity"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason"`
	OpenedAt       time.Time  `json:"opened_at"`
	SourceEventID  *string    `json:"source_event_id"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	ResolutionNote *string    `json:"resolution_note"`
	ResolvedBy     *string    `json:"resolved_by"`
}

func toAlertDTO(alert domain.Alert) alertDTO {
	return alertDTO{
		AlertID:        alert.AlertID,
		AssetID:        alert.AssetID,
		SiteID:         alert.SiteID,
		Kind:           alert.Kind,
		Severity:       string(alert.Severity),
		Status:         string(alert.Status),
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
	SiteID     string         `json:"site_id"`
	AssetCount int64          `json:"asset_count"`
	LastSeenAt *time.Time     `json:"last_seen_at"`
	OpenAlerts alertCountsDTO `json:"open_alerts"`
}

type alertCountsDTO struct {
	Total    int64 `json:"total"`
	Critical int64 `json:"critical"`
	Warning  int64 `json:"warning"`
	Info     int64 `json:"info"`
}

func toSiteDTO(site domain.Site) siteDTO {
	return siteDTO{
		SiteID:     site.SiteID,
		AssetCount: site.AssetCount,
		LastSeenAt: site.LastSeenAt,
		OpenAlerts: alertCountsDTO{
			Total:    site.OpenAlerts.Total,
			Critical: site.OpenAlerts.Critical,
			Warning:  site.OpenAlerts.Warning,
			Info:     site.OpenAlerts.Info,
		},
	}
}
