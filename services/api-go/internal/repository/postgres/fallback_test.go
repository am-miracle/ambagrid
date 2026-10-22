package postgres

import "api-go/internal/repository"

var _ repository.FallbackTx = fallbackTx{}
