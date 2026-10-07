package collector

import (
	"context"

	"edge-agent-go/internal/domain"
)

type Source interface {
	Read(context.Context) ([]domain.Reading, error)
}

type Queue interface {
	Persist(context.Context, domain.Event) (uint64, error)
	MarkPendingUpload(context.Context, uint64) error
}
