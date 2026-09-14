// defines the service interfaces used by HTTP handlers.
package controller

import (
	"context"

	"api-go/internal/domain"
	"api-go/internal/page"
	"api-go/internal/services"
)

// arrow interfaces keep handlers independent from service implementations.

type AssetService interface {
	List(ctx context.Context, request services.ListAssetsRequest) (page.Page[domain.Asset], error)
	Get(ctx context.Context, assetID string) (domain.Asset, error)
	Readings(ctx context.Context, assetID string, request services.GetReadingSeriesRequest) (domain.ReadingSeries, error)
}

type AlertService interface {
	List(ctx context.Context, request services.ListAlertsRequest) (page.Page[domain.Alert], error)
	Get(ctx context.Context, alertID string) (services.AlertDetail, error)
	Resolve(ctx context.Context, alertID string, request services.ResolveAlertRequest) (domain.Alert, error)
}

type SiteService interface {
	List(ctx context.Context, request services.ListSitesRequest) (page.Page[domain.Site], error)
}

type HealthService interface {
	Ready(ctx context.Context) error
}
