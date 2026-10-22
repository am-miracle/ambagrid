package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/repository"
)

type fakeHeartbeatTx struct {
	stored          *domain.StoredHeartbeat
	receiptInserted bool
	receiptKey      domain.HeartbeatKey
	monthlyUsage    int64

	insertedContactAt *time.Time
	usageReads        int
}

func (f *fakeHeartbeatTx) FindHeartbeat(context.Context, domain.HeartbeatKey) (*domain.StoredHeartbeat, error) {
	return f.stored, nil
}

func (f *fakeHeartbeatTx) InsertHeartbeat(_ context.Context, _ domain.GatewayHeartbeat, contactAt time.Time) error {
	f.insertedContactAt = &contactAt
	return nil
}

func (f *fakeHeartbeatTx) InsertHeartbeatReceipt(context.Context, domain.HeartbeatKey, domain.SMSReceipt, domain.SMSPricing) (bool, error) {
	return f.receiptInserted, nil
}

func (f *fakeHeartbeatTx) FindHeartbeatReceiptKey(context.Context, string) (domain.HeartbeatKey, error) {
	return f.receiptKey, nil
}

func (f *fakeHeartbeatTx) GetMonthlySMSUsage(context.Context, time.Time, int64) (int64, error) {
	f.usageReads++
	return f.monthlyUsage, nil
}

type fakeHeartbeatRepository struct{ tx *fakeHeartbeatTx }

func (f *fakeHeartbeatRepository) RunHeartbeatTx(_ context.Context, _ domain.HeartbeatKey, fn func(repository.HeartbeatTx) (bool, error)) (bool, error) {
	return fn(f.tx)
}

func testHeartbeat() domain.GatewayHeartbeat {
	return domain.GatewayHeartbeat{
		SiteID: "site-01", GatewayID: "gateway-01", Sequence: 42, Interval: 30 * time.Minute,
		SentAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func newTestHeartbeatService(tx *fakeHeartbeatTx, pricing domain.SMSPricing) *HeartbeatService {
	return NewFallbackService(&fakeFallbackRepository{}, "alert.opened", pricing).
		Heartbeats(&fakeHeartbeatRepository{tx: tx})
}

func TestHeartbeatServiceRecordsContactBoundedByBothClocks(t *testing.T) {
	heartbeat := testHeartbeat()
	tests := map[string]struct {
		receivedAt time.Time
		want       time.Time
	}{
		"delayed delivery uses send time":       {receivedAt: heartbeat.SentAt.Add(3 * time.Hour), want: heartbeat.SentAt},
		"gateway clock ahead uses receive time": {receivedAt: heartbeat.SentAt.Add(-time.Hour), want: heartbeat.SentAt.Add(-time.Hour)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			tx := &fakeHeartbeatTx{receiptInserted: true}
			created, err := newTestHeartbeatService(tx, domain.SMSPricing{}).
				ReceiveHeartbeat(context.Background(), heartbeat, domain.SMSReceipt{ProviderMessageID: "msg-01", ReceivedAt: test.receivedAt})
			if err != nil || !created {
				t.Fatalf("ReceiveHeartbeat() = %v, %v; want created", created, err)
			}
			if tx.insertedContactAt == nil || !tx.insertedContactAt.Equal(test.want) {
				t.Fatalf("contact_at = %v, want %v", tx.insertedContactAt, test.want)
			}
		})
	}
}

func TestHeartbeatServiceDeduplicatesRepeatedHeartbeat(t *testing.T) {
	heartbeat := testHeartbeat()
	tx := &fakeHeartbeatTx{stored: &domain.StoredHeartbeat{SentAt: heartbeat.SentAt, Interval: heartbeat.Interval}, receiptInserted: true}
	created, err := newTestHeartbeatService(tx, domain.SMSPricing{}).
		ReceiveHeartbeat(context.Background(), heartbeat, domain.SMSReceipt{ProviderMessageID: "msg-02", ReceivedAt: heartbeat.SentAt})
	if err != nil || created {
		t.Fatalf("ReceiveHeartbeat() = %v, %v; want duplicate accepted without creation", created, err)
	}
	if tx.insertedContactAt != nil {
		t.Fatal("duplicate heartbeat re-inserted liveness")
	}
}

func TestHeartbeatServiceRejectsConflictingReuse(t *testing.T) {
	heartbeat := testHeartbeat()
	t.Run("same key with different send time", func(t *testing.T) {
		tx := &fakeHeartbeatTx{stored: &domain.StoredHeartbeat{SentAt: heartbeat.SentAt.Add(time.Minute), Interval: heartbeat.Interval}}
		_, err := newTestHeartbeatService(tx, domain.SMSPricing{}).
			ReceiveHeartbeat(context.Background(), heartbeat, domain.SMSReceipt{ProviderMessageID: "msg-01"})
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("error = %v, want conflict", err)
		}
	})
	t.Run("same key with different declared interval", func(t *testing.T) {
		tx := &fakeHeartbeatTx{stored: &domain.StoredHeartbeat{SentAt: heartbeat.SentAt, Interval: time.Hour}}
		_, err := newTestHeartbeatService(tx, domain.SMSPricing{}).
			ReceiveHeartbeat(context.Background(), heartbeat, domain.SMSReceipt{ProviderMessageID: "msg-01"})
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("error = %v, want conflict", err)
		}
	})
	t.Run("provider message reused for another heartbeat", func(t *testing.T) {
		tx := &fakeHeartbeatTx{receiptKey: domain.NewHeartbeatKey("site-01", "gateway-01", 41)}
		_, err := newTestHeartbeatService(tx, domain.SMSPricing{}).
			ReceiveHeartbeat(context.Background(), heartbeat, domain.SMSReceipt{ProviderMessageID: "msg-01"})
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("error = %v, want conflict", err)
		}
	})
}

func TestHeartbeatServiceCountsTowardSMSBudget(t *testing.T) {
	tx := &fakeHeartbeatTx{receiptInserted: true, monthlyUsage: 900}
	heartbeat := testHeartbeat()
	if _, err := newTestHeartbeatService(tx, domain.SMSPricing{MonthlyBudgetMinor: 1000}).
		ReceiveHeartbeat(context.Background(), heartbeat, domain.SMSReceipt{ProviderMessageID: "msg-01", ReceivedAt: heartbeat.SentAt}); err != nil {
		t.Fatal(err)
	}
	if tx.usageReads != 1 {
		t.Fatalf("budget usage reads = %d, want 1", tx.usageReads)
	}
}
