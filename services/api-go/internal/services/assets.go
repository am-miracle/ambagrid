// applies asset listing and lookup rules.
package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
)

// AssetService applies asset read rules.
type AssetService struct {
	assets AssetRepository
	limits PageLimits
}

func NewAssetService(assets AssetRepository, limits PageLimits) *AssetService {
	return &AssetService{assets: assets, limits: limits}
}

type ListAssetsRequest struct {
	Filter domain.AssetFilter
	// Limit uses the default page size when zero.
	Limit  int
	Cursor string
}

func (s *AssetService) List(ctx context.Context, request ListAssetsRequest) (page.Page[domain.Asset], error) {
	limit, err := s.limits.Resolve(request.Limit)
	if err != nil {
		return page.Page[domain.Asset]{}, err
	}

	return s.assets.ListAssets(ctx, domain.AssetQuery{
		Filter: request.Filter,
		Limit:  limit,
		Cursor: request.Cursor,
	})
}

func (s *AssetService) Get(ctx context.Context, assetID string) (domain.Asset, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return domain.Asset{}, fmt.Errorf("%w: asset_id must not be empty", domain.ErrInvalidID)
	}

	return s.assets.GetAsset(ctx, assetID)
}

type GetReadingSeriesRequest struct {
	From     time.Time
	To       time.Time
	Metric   domain.ReadingMetric
	Interval domain.ReadingInterval
}

func (s *AssetService) Readings(ctx context.Context, assetID string, request GetReadingSeriesRequest) (domain.ReadingSeries, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return domain.ReadingSeries{}, fmt.Errorf("%w: asset_id must not be empty", domain.ErrInvalidID)
	}
	if !request.From.Before(request.To) {
		return domain.ReadingSeries{}, fmt.Errorf("%w: from must be before to", ErrInvalidRequest)
	}
	if request.Interval.MaximumWindow() == 0 {
		return domain.ReadingSeries{}, fmt.Errorf("%w: interval is required", ErrInvalidRequest)
	}
	if request.To.Sub(request.From) > request.Interval.MaximumWindow() {
		return domain.ReadingSeries{}, fmt.Errorf("%w: interval %s supports at most %s", ErrInvalidRequest, request.Interval, request.Interval.MaximumWindow())
	}
	asset, err := s.assets.GetAsset(ctx, assetID)
	if err != nil {
		return domain.ReadingSeries{}, err
	}
	if !request.Metric.Supports(asset.AssetType) {
		return domain.ReadingSeries{}, fmt.Errorf("%w: metric %q is unavailable for asset type %q", ErrInvalidRequest, request.Metric, asset.AssetType)
	}

	return s.assets.GetReadingSeries(ctx, domain.ReadingQuery{
		AssetID:   assetID,
		AssetType: asset.AssetType,
		From:      request.From.UTC(),
		To:        request.To.UTC(),
		Metric:    request.Metric,
		Interval:  request.Interval,
	})
}
