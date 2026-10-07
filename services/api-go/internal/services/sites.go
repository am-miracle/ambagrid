// Applies site listing rules.
package services

import (
	"context"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
)

// SiteService applies site listing rules.
type SiteService struct {
	sites           SiteRepository
	limits          PageLimits
	offlineAfter    time.Duration
	eventStaleAfter time.Duration
	now             func() time.Time
}

func NewSiteService(sites SiteRepository, limits PageLimits, offlineAfter, eventStaleAfter time.Duration) *SiteService {
	return &SiteService{
		sites: sites, limits: limits,
		offlineAfter: offlineAfter, eventStaleAfter: eventStaleAfter,
		now: time.Now,
	}
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

	result, err := s.sites.ListSites(ctx, domain.SiteQuery{
		Limit:  limit,
		Cursor: request.Cursor,
	})
	if err != nil {
		return page.Page[domain.Site]{}, err
	}

	now := s.now().UTC()
	for i := range result.Items {
		result.Items[i].HealthStatus = siteHealthStatus(result.Items[i], now, s.offlineAfter, s.eventStaleAfter)
	}
	return result, nil
}

func siteHealthStatus(site domain.Site, now time.Time, offlineAfter, eventStaleAfter time.Duration) domain.SiteHealthStatus {
	if site.LastContactAt == nil || site.LastContactAt.Before(now.Add(-offlineAfter)) {
		return domain.SiteHealthOffline
	}
	if site.LastEventAt == nil || site.LastEventAt.Before(now.Add(-eventStaleAfter)) || site.QueueGrowing {
		return domain.SiteHealthDelayed
	}
	return domain.SiteHealthLive
}
