// Defines the repository interfaces used by services.
package services

import (
	"context"

	"api-go/internal/domain"
	"api-go/internal/page"
)

// Services depend on these interfaces instead of Postgres directly.

type AssetRepository interface {
	ListAssets(ctx context.Context, query domain.AssetQuery) (page.Page[domain.Asset], error)
	GetAsset(ctx context.Context, assetID string) (domain.Asset, error)
}

type AlertRepository interface {
	ListAlerts(ctx context.Context, query domain.AlertQuery) (page.Page[domain.Alert], error)
	GetAlertWithResolutions(ctx context.Context, alertID string) (domain.Alert, []domain.AlertResolution, error)
}

type SiteRepository interface {
	ListSites(ctx context.Context, query domain.SiteQuery) (page.Page[domain.Site], error)
}

type HealthRepository interface {
	Ping(ctx context.Context) error
}
