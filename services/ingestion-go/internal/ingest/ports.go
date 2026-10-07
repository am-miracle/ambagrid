package ingest

import (
	"context"
	"net/http"
)

type Authenticator interface {
	Authenticate(r *http.Request) (siteID string, err error)
}

type DedupStore interface {
	TryInsert(ctx context.Context, siteID string, sequence uint64) (inserted bool, err error)
	Remove(ctx context.Context, siteID string, sequence uint64) error
}

type Producer interface {
	Produce(ctx context.Context, siteID string, rec IngestRecord) error
}
