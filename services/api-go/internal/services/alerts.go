// Applies alert listing and detail rules.
package services

import (
	"context"

	"api-go/internal/domain"
	"api-go/internal/page"
)

// AlertService applies alert read rules.
type AlertService struct {
	alerts        AlertRepository
	limits        PageLimits
	globalHistory PageLimits
}

func NewAlertService(alerts AlertRepository, limits, globalHistory PageLimits) *AlertService {
	return &AlertService{alerts: alerts, limits: limits, globalHistory: globalHistory}
}

type ListAlertsRequest struct {
	Filter domain.AlertFilter
	// Limit uses the default page size when zero.
	Limit  int
	Cursor string
}

// AlertDetail combines an alert with its resolution history.
type AlertDetail struct {
	Alert       domain.Alert
	Resolutions []domain.AlertResolution
}

func (s *AlertService) List(ctx context.Context, request ListAlertsRequest) (page.Page[domain.Alert], error) {
	filter := request.Filter

	// Unscoped history scans broadly, so it uses tighter limits.
	limits := s.limits
	if filter.SiteID == nil && filter.AssetID == nil {
		if filter.Status == nil {
			open := domain.AlertStatusOpen
			filter.Status = &open
		} else if *filter.Status != domain.AlertStatusOpen {
			limits = s.globalHistory
		}
	}

	limit, err := limits.Resolve(request.Limit)
	if err != nil {
		return page.Page[domain.Alert]{}, err
	}

	return s.alerts.ListAlerts(ctx, domain.AlertQuery{
		Filter: filter,
		Limit:  limit,
		Cursor: request.Cursor,
	})
}

func (s *AlertService) Get(ctx context.Context, alertID string) (AlertDetail, error) {
	if err := domain.ValidateAlertID(alertID); err != nil {
		return AlertDetail{}, err
	}

	alert, resolutions, err := s.alerts.GetAlertWithResolutions(ctx, alertID)
	if err != nil {
		return AlertDetail{}, err
	}

	return AlertDetail{Alert: alert, Resolutions: resolutions}, nil
}
