package fallback_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"edge-agent-go/internal/fallback"
)

type fakeStore struct {
	candidates []fallback.Candidate
	attempts   []fallback.AttemptResult
	reserveErr error
}

func (s *fakeStore) FallbackCandidates(context.Context, time.Time) ([]fallback.Candidate, error) {
	return s.candidates, nil
}

func (s *fakeStore) RecordFallbackAttempt(_ context.Context, result fallback.AttemptResult) error {
	s.attempts = append(s.attempts, result)
	return nil
}

func (s *fakeStore) ReserveFallbackAttempt(context.Context, string, time.Time) error {
	return s.reserveErr
}

type fakeSender struct{ messages []string }

func (s *fakeSender) Send(_ context.Context, destination, message string) error {
	s.messages = append(s.messages, destination+" "+message)
	return nil
}

func TestCoordinatorSendsEligibleCriticalIncident(t *testing.T) {
	now := time.Unix(1730000200, 0).UTC()
	store := &fakeStore{candidates: []fallback.Candidate{{
		Event: fallback.Event{
			SiteID: "ng-kaji-01", AssetID: "batt-01", Sequence: 18422,
			Code: fallback.BatteryOverheat, TemperatureC: ptr(68.2),
			OccurredAt: time.Unix(1730000000, 0).UTC(),
		},
		OpenedAt: now.Add(-time.Minute), UploadFailures: 3,
	}}}
	sender := &fakeSender{}
	coordinator := fallback.NewCoordinator(fallback.Config{
		Destination: "+2348000000000", FailureThreshold: 3,
		OfflineAfter: 2 * time.Minute, MaxAttempts: 3,
		HourlyLimit: 6, DailyLimit: 20, RetryInterval: 5 * time.Minute,
	}, store, sender)
	coordinator.SetClock(func() time.Time { return now })

	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("sent messages = %v, want one", sender.messages)
	}
	if len(store.attempts) != 1 || !store.attempts[0].Succeeded {
		t.Fatalf("attempts = %+v, want one success", store.attempts)
	}
}

func TestCoordinatorWaitsForConnectivityThreshold(t *testing.T) {
	now := time.Unix(1730000200, 0).UTC()
	store := &fakeStore{candidates: []fallback.Candidate{{
		Event: fallback.Event{
			SiteID: "ng-kaji-01", AssetID: "inv-01", Sequence: 9,
			Code: fallback.InverterFailure, OccurredAt: now.Add(-time.Minute),
		},
		OpenedAt: now.Add(-time.Minute), UploadFailures: 2, LastUploadSuccess: ptrTime(now.Add(-time.Minute)),
	}}}
	sender := &fakeSender{}
	coordinator := fallback.NewCoordinator(fallback.Config{
		Destination: "+2348000000000", FailureThreshold: 3,
		OfflineAfter: 2 * time.Minute, MaxAttempts: 3,
		HourlyLimit: 6, DailyLimit: 20, RetryInterval: 5 * time.Minute,
	}, store, sender)
	coordinator.SetClock(func() time.Time { return now })

	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(sender.messages) != 0 || len(store.attempts) != 0 {
		t.Fatalf("sent=%v attempts=%v, want none", sender.messages, store.attempts)
	}
}

func TestCoordinatorEnforcesSiteBudgetAcrossCandidates(t *testing.T) {
	now := time.Unix(1730000200, 0).UTC()
	store := &fakeStore{candidates: []fallback.Candidate{
		{Event: fallback.Event{SiteID: "site-01", AssetID: "inv-01", Sequence: 1, Code: fallback.InverterFailure, OccurredAt: now}, OpenedAt: now, UploadFailures: 3, AttemptsLastHour: 5},
		{Event: fallback.Event{SiteID: "site-01", AssetID: "inv-02", Sequence: 2, Code: fallback.InverterFailure, OccurredAt: now}, OpenedAt: now, UploadFailures: 3, AttemptsLastHour: 5},
	}}
	sender := &fakeSender{}
	coordinator := fallback.NewCoordinator(fallback.Config{Destination: "+2348000000000", FailureThreshold: 3, OfflineAfter: time.Minute, MaxAttempts: 3, HourlyLimit: 6, DailyLimit: 20}, store, sender)
	coordinator.SetClock(func() time.Time { return now })
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("sent %d messages, want one remaining site-budget attempt", len(sender.messages))
	}
}

func TestCoordinatorDoesNotSendWhenAttemptCannotBeReserved(t *testing.T) {
	now := time.Unix(1730000200, 0).UTC()
	store := &fakeStore{reserveErr: errors.New("database unavailable"), candidates: []fallback.Candidate{{
		Event:    fallback.Event{SiteID: "site-01", AssetID: "inv-01", Sequence: 1, Code: fallback.InverterFailure, OccurredAt: now},
		OpenedAt: now, UploadFailures: 3,
	}}}
	sender := &fakeSender{}
	coordinator := fallback.NewCoordinator(fallback.Config{Destination: "+2348000000000", FailureThreshold: 3, MaxAttempts: 3, HourlyLimit: 6, DailyLimit: 20}, store, sender)
	coordinator.SetClock(func() time.Time { return now })
	if err := coordinator.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil")
	}
	if len(sender.messages) != 0 {
		t.Fatalf("messages = %v, want none", sender.messages)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
