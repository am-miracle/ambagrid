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
	sites      SiteRepository
	limits     PageLimits
	thresholds SiteHealthThresholds
	now        func() time.Time
}

type SiteHealthThresholds struct {
	OfflineAfter    time.Duration
	SMSOfflineAfter time.Duration
	EventStaleAfter time.Duration
}

func NewSiteService(sites SiteRepository, limits PageLimits, thresholds SiteHealthThresholds) *SiteService {
	return &SiteService{sites: sites, limits: limits, thresholds: thresholds, now: time.Now}
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
		result.Items[i].HealthStatus = siteHealthStatus(result.Items[i], now, s.thresholds)
	}
	return result, nil
}

// Offline is always inferred from silence on both channels. SMS heartbeats are
// sparse, so they get their own threshold, and an SMS-only gateway is at best
// delayed because no telemetry is flowing.
func siteHealthStatus(site domain.Site, now time.Time, thresholds SiteHealthThresholds) domain.SiteHealthStatus {
	httpFresh := site.LastContactAt != nil && !site.LastContactAt.Before(now.Add(-thresholds.OfflineAfter))
	if !httpFresh {
		if site.LastSMSHeartbeatAt != nil && !site.LastSMSHeartbeatAt.Before(now.Add(-thresholds.SMSOfflineAfter)) {
			return domain.SiteHealthDelayed
		}
		return domain.SiteHealthOffline
	}
	if site.LastEventAt == nil || site.LastEventAt.Before(now.Add(-thresholds.EventStaleAfter)) || site.QueueGrowing {
		return domain.SiteHealthDelayed
	}
	return domain.SiteHealthLive
}
