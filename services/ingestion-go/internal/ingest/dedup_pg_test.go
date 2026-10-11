package ingest_test

import (
	"testing"

	"ingestion-go/internal/ingest"
)

func TestPostgresDedupStore_ImplementsDedupStore(t *testing.T) {
	var _ ingest.DedupStore = (*ingest.PostgresDedupStore)(nil)
}
