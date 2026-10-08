// dedup.go tracks seen (site_id, sequence) pairs in memory. This is sufficient
// for single-instance deployments; a multi-replica setup would swap this for a
// Redis or database-backed implementation behind the DedupStore interface.
package ingest

import (
	"context"
	"sync"
)

type MemoryDedupStore struct {
	mu   sync.Mutex
	seen map[dedupKey]struct{}
}

type dedupKey struct {
	SiteID   string
	Sequence uint64
}

func NewMemoryDedupStore() *MemoryDedupStore {
	return &MemoryDedupStore{seen: make(map[dedupKey]struct{})}
}

func (s *MemoryDedupStore) TryInsert(_ context.Context, siteID string, sequence uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dedupKey{SiteID: siteID, Sequence: sequence}
	if _, exists := s.seen[k]; exists {
		return false, nil
	}
	s.seen[k] = struct{}{}
	return true, nil
}

func (s *MemoryDedupStore) Remove(_ context.Context, siteID string, sequence uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seen, dedupKey{SiteID: siteID, Sequence: sequence})
	return nil
}
