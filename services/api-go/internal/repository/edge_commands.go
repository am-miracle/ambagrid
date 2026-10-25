package repository

import (
	"context"
	"time"

	"api-go/internal/domain"
)

type EdgeCommandRepository interface {
	RunEdgeCommandTx(ctx context.Context, fn func(EdgeCommandTx) error) error
}

type EdgeCommandTx interface {
	ExpireOverdueCommands(ctx context.Context, siteID string, now time.Time) ([]domain.MeterCommand, error)
	ListPendingEdgeCommands(ctx context.Context, siteID string, now time.Time, limit int) ([]domain.EdgeCommand, error)
	MarkCommandsQueued(ctx context.Context, commandIDs []string) error
	// LockSiteCommand returns domain.ErrNotFound for a command belonging to another site.
	LockSiteCommand(ctx context.Context, siteID, commandID string) (domain.MeterCommand, error)
	SaveCommandStatus(ctx context.Context, cmd domain.MeterCommand) error
	PublishCommandOutcome(ctx context.Context, topic, eventType string, cmd domain.MeterCommand, siteID, reportedBy string, occurredAt time.Time) error
	InsertAuditEvent(ctx context.Context, siteID, actorID, action, subjectType, subjectID string, metadata map[string]any) error
}
