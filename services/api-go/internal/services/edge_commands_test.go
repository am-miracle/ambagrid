package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
	"api-go/internal/repository"
)

type reconciliationTx struct {
	repository.EdgeCommandTx
	current   domain.MeterCommand
	retained  *domain.CommandReport
	retainErr error
}

func (t *reconciliationTx) LockSiteCommand(context.Context, string, string) (domain.MeterCommand, error) {
	return t.current, nil
}

func (t *reconciliationTx) RetainConflictingReport(_ context.Context, _ string, _ domain.MeterCommand, report domain.CommandReport) error {
	t.retained = &report
	return t.retainErr
}

type reconciliationRepository struct {
	repository.EdgeCommandRepository
	tx        repository.EdgeCommandTx
	commitErr error
	items     []domain.CommandReconciliation
	listCalls int
	gotLimit  int
}

func (r *reconciliationRepository) RunEdgeCommandTx(ctx context.Context, fn func(repository.EdgeCommandTx) error) error {
	if err := fn(r.tx); err != nil {
		return err
	}
	return r.commitErr
}

func (r *reconciliationRepository) ListCommandReconciliation(_ context.Context, _ string, _ time.Time, limit int, _ *page.TimeIDCursor) (page.Page[domain.CommandReconciliation], error) {
	r.listCalls++
	r.gotLimit = limit
	return page.Page[domain.CommandReconciliation]{Items: r.items, Limit: limit}, nil
}

func TestConflictingReportRetainsOriginalEvidenceBeforeAccepting(t *testing.T) {
	const id = "3f2b8c1e-5d4a-4e6b-9c7d-1a2b3c4d5e6f"
	now := time.Now().UTC()
	report := domain.CommandReport{CommandID: id, Status: domain.CommandStatusSent, OccurredAt: now.Add(time.Hour)}
	storageErr := errors.New("storage unavailable")
	for _, failure := range []string{"none", "write", "commit"} {
		t.Run(failure, func(t *testing.T) {
			tx := &reconciliationTx{current: domain.MeterCommand{
				CommandID: id, Status: domain.CommandStatusExpired, RequestedAt: now.Add(-time.Hour),
			}}
			repo := &reconciliationRepository{tx: tx}
			if failure == "write" {
				tx.retainErr = storageErr
			} else if failure == "commit" {
				repo.commitErr = storageErr
			}
			s := NewEdgeCommandService(repo, OutcomeTopics{}, SiteHealthThresholds{})
			s.now = func() time.Time { return now }
			cmd, changed, err := s.Report(context.Background(), "site-01", report)
			if tx.retained == nil || !tx.retained.OccurredAt.Equal(report.OccurredAt) {
				t.Fatalf("original timestamp was not preserved: %+v", tx.retained)
			}
			if failure == "none" {
				if !errors.Is(err, ErrReportNeedsReconciliation) || changed || cmd.Status != domain.CommandStatusExpired {
					t.Fatalf("cmd=%+v changed=%t err=%v", cmd, changed, err)
				}
			} else if !errors.Is(err, storageErr) || errors.Is(err, ErrReportNeedsReconciliation) {
				t.Fatalf("uncommitted report accepted: %v", err)
			}
		})
	}
}

func TestReconciliationReportsAgeAndUsesSiteHealthRules(t *testing.T) {
	now := time.Now().UTC()
	repo := &reconciliationRepository{items: []domain.CommandReconciliation{{
		SiteCommand: domain.SiteCommand{Command: domain.MeterCommand{RequestedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}},
		Site:        domain.Site{SMSHeartbeatExpiresAt: new(now.Add(time.Hour))},
	}}}
	s := NewEdgeCommandService(repo, OutcomeTopics{}, SiteHealthThresholds{OfflineAfter: time.Minute, EventStaleAfter: time.Minute})
	s.now = func() time.Time { return now }
	result, err := s.ListReconciliation(context.Background(), "site-01", 1000, "")
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	item := result.Items[0]
	if repo.gotLimit != maxEdgeCommandBatch || item.Age != 2*time.Hour || item.Overdue != time.Hour || item.Site.HealthStatus != domain.SiteHealthDelayed {
		t.Fatalf("limit=%d item=%+v", repo.gotLimit, item)
	}
	_, err = s.ListReconciliation(context.Background(), "site-01", 0, "bad-cursor")
	if !errors.Is(err, page.ErrInvalidCursor) || repo.listCalls != 1 {
		t.Fatalf("invalid cursor reached the repository: calls=%d err=%v", repo.listCalls, err)
	}
}
