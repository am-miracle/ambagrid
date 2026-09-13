// applies asset listing and lookup rules.
package services

import (
	"context"
	"fmt"
	"strings"

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
