package collector

import (
	"context"

	"edge-agent-go/internal/domain"
)

type Source interface {
	Read(context.Context) ([]domain.Reading, error)
}
