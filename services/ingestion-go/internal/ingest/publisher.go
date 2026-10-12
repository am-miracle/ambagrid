package ingest

import (
	"context"
	"sync"
)

type claimKey struct {
	SiteID   string
	Sequence uint64
}

type claim struct {
	done chan struct{}
	ok   bool
}

type OncePublisher struct {
	producer Producer
	mu       sync.Mutex
	claims   map[claimKey]*claim
}

func NewOncePublisher(producer Producer) *OncePublisher {
	return &OncePublisher{
		producer: producer,
		claims:   make(map[claimKey]*claim),
	}
}

func (p *OncePublisher) PublishOnce(ctx context.Context, siteID string, rec IngestRecord) (bool, error) {
	key := claimKey{SiteID: siteID, Sequence: rec.Sequence}

	for {
		p.mu.Lock()
		c, exists := p.claims[key]
		if !exists {
			c = &claim{done: make(chan struct{})}
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
