// Defines alerts, resolution history, and alert filters.
package domain

import (
	"fmt"
	"time"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

func ParseSeverity(value string) (Severity, error) {
	switch Severity(value) {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return Severity(value), nil
	default:
		return "", fmt.Errorf("unknown severity: %q", value)
	}
}

type AlertStatus string

const (
	AlertStatusOpen     AlertStatus = "open"
	AlertStatusResolved AlertStatus = "resolved"
)

func ParseAlertStatus(value string) (AlertStatus, error) {
	switch AlertStatus(value) {
	case AlertStatusOpen, AlertStatusResolved:
		return AlertStatus(value), nil
	default:
		return "", fmt.Errorf("unknown status: %q", value)
	}
}

type Alert struct {
	AlertID        string
	AssetID        string
	SiteID         string
	Kind           string
	Severity       Severity
	Status         AlertStatus
	Reason         string
	OpenedAt       time.Time
	SourceEventID  *string
	ResolvedAt     *time.Time
	ResolutionNote *string
	ResolvedBy     *string
}

// alertResolution records one resolution in an alert's lifecycle.
type AlertResolution struct {
	ResolutionID   string
	ResolvedAt     time.Time
	ResolutionNote string
	ResolvedBy     string
	RecordedAt     time.Time
}

// AlertFilter narrows an alert listing. A nil field means "no filter".
type AlertFilter struct {
	SiteID   *string
	AssetID  *string
	Status   *AlertStatus
	Severity *Severity
	Kind     *string
}
