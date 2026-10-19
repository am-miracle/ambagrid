package ingest

import (
	"context"
	"sync"
	"time"
)

type claimKey struct {
	SiteID   string
	Sequence uint64
}

type claim struct {
	done      chan struct{}
	ok        bool
	claimedAt time.Time
}

type OncePublisher struct {
	producer      Producer
	mu            sync.Mutex
	claims        map[claimKey]*claim
	ttl           time.Duration
	sweepInterval time.Duration
}

func NewOncePublisher(producer Producer, opts ...OncePublisherOption) *OncePublisher {
	p := &OncePublisher{
		producer:      producer,
		claims:        make(map[claimKey]*claim),
		ttl:           10 * time.Minute,
		sweepInterval: 1 * time.Minute,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

type OncePublisherOption func(*OncePublisher)

func WithClaimTTL(d time.Duration) OncePublisherOption {
	return func(p *OncePublisher) { p.ttl = d }
}

func WithSweepInterval(d time.Duration) OncePublisherOption {
	return func(p *OncePublisher) { p.sweepInterval = d }
}

func (p *OncePublisher) RunEviction(ctx context.Context) {
	ticker := time.NewTicker(p.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.evictExpired()
		}
	}
}

func (p *OncePublisher) evictExpired() {
	cutoff := time.Now().Add(-p.ttl)
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, c := range p.claims {
		select {
		case <-c.done:
			if c.claimedAt.Before(cutoff) {
				delete(p.claims, k)
			}
		default:
			// In-flight produce — leave it alone.
		}
	}
}

func (p *OncePublisher) PublishOnce(ctx context.Context, siteID string, rec IngestRecord) (bool, error) {
	key := claimKey{SiteID: siteID, Sequence: rec.Sequence}

	for {
		p.mu.Lock()
		c, exists := p.claims[key]
		if !exists {
			c = &claim{done: make(chan struct{}), claimedAt: time.Now()}
			p.claims[key] = c
			p.mu.Unlock()

			err := p.producer.Produce(ctx, siteID, rec)

			p.mu.Lock()
			if err != nil {
				delete(p.claims, key)
			}
			c.ok = err == nil
			close(c.done)
			p.mu.Unlock()

			return false, err
		}
		p.mu.Unlock()

		select {
		case <-c.done:
			if c.ok {
				return true, nil
			}
			continue
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}
