package postgres

import "api-go/internal/services"

var _ services.FallbackTx = fallbackTx{}
