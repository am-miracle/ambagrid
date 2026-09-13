// Applies site listing rules.
package services

import (
	"context"

	"api-go/internal/domain"
	"api-go/internal/page"
)

// SiteService applies site listing rules.
type SiteService struct {
	sites  SiteRepository
	limits PageLimits
}

func NewSiteService(sites SiteRepository, limits PageLimits) *SiteService {
	return &SiteService{sites: sites, limits: limits}
}

type ListSitesRequest struct {
	// Limit uses the default page size when zero.
	Limit  int
	Cursor string
}

func (s *SiteService) List(ctx context.Context, request ListSitesRequest) (page.Page[domain.Site], error) {
	limit, err := s.limits.Resolve(request.Limit)
	if err != nil {
		return page.Page[domain.Site]{}, err
	}

	return s.sites.ListSites(ctx, domain.SiteQuery{
		Limit:  limit,
		Cursor: request.Cursor,
	})
}
