package ingest

import (
	"context"
	"net/http"
	"time"
)

type Authenticator interface {
	Authenticate(r *http.Request) (siteID string, err error)
}

type Producer interface {
	Produce(ctx context.Context, siteID string, rec IngestRecord) error
}

type Publisher interface {
	PublishOnce(ctx context.Context, siteID string, rec IngestRecord) (duplicate bool, err error)
}

type SiteHealthReport struct {
	SiteID             string
	GatewayID          string
	ContactAt          time.Time
	LastEventTimestamp *time.Time
	QueueDepth         int64
	OldestPendingAt    *time.Time
}

type SiteHealthStore interface {
	RecordContact(ctx context.Context, report SiteHealthReport) error
}
