// Provides the API readiness check.
package services

import "context"

// HealthService reports whether the database is reachable.
type HealthService struct {
	database HealthRepository
}

func NewHealthService(database HealthRepository) *HealthService {
	return &HealthService{database: database}
}

// Ready reports whether this replica can serve database-backed requests.
func (s *HealthService) Ready(ctx context.Context) error {
	return s.database.Ping(ctx)
}
