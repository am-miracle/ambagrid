package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/repository"
)

type fakeFallbackTx struct {
	storedEvent      *domain.StoredFallbackEvent
	assetType        string
	lookupAssetErr   error
	sourceAlertID    string
	sourceAlertErr   error
	openAlertID      string
	openAlertErr     error
	createdAlertID   string
	createAlertErr   error
	insertEventErr   error
	insertedReturns  bool
	insertReceiptErr error
	receiptEventKey  string
	findReceiptErr   error
	monthlyUsage     int64
	usageErr         error

	outboxCalls      int
	eventInsertCalls int
}

func (f *fakeFallbackTx) FindFallbackEvent(context.Context, domain.EventKey) (*domain.StoredFallbackEvent, error) {
	return f.storedEvent, nil
}

func (f *fakeFallbackTx) LookupAssetType(context.Context, string, string) (string, error) {
	return f.assetType, f.lookupAssetErr
}

func (f *fakeFallbackTx) FindAlertBySourceEvent(context.Context, domain.EventKey) (string, error) {
	if f.sourceAlertErr != nil {
		return "", f.sourceAlertErr
	}
	if f.sourceAlertID == "" {
		return "", domain.ErrNotFound
	}
	return f.sourceAlertID, nil
}

func (f *fakeFallbackTx) FindOpenAlert(context.Context, string, string, string) (string, error) {
	if f.openAlertErr != nil {
		return "", f.openAlertErr
	}
	if f.openAlertID == "" {
		return "", domain.ErrNotFound
	}
	return f.openAlertID, nil
}

func (f *fakeFallbackTx) CreateAlert(context.Context, domain.CreateAlertCommand) (string, error) {
	if f.createAlertErr != nil {
		return "", f.createAlertErr
	}
	return f.createdAlertID, nil
}

func (f *fakeFallbackTx) PublishOutboxEvent(context.Context, string, string, string, string, []byte) error {
	f.outboxCalls++
	return nil
}

func (f *fakeFallbackTx) InsertFallbackEvent(context.Context, domain.InsertFallbackEventCommand) error {
	f.eventInsertCalls++
	return f.insertEventErr
}

func (f *fakeFallbackTx) InsertReceipt(context.Context, domain.EventKey, domain.SMSReceipt, domain.SMSPricing) (bool, error) {
	return f.insertedReturns, f.insertReceiptErr
}

func (f *fakeFallbackTx) FindReceiptEventKey(context.Context, string) (domain.EventKey, error) {
	return domain.EventKey(f.receiptEventKey), f.findReceiptErr
}

func (f *fakeFallbackTx) GetMonthlySMSUsage(context.Context, time.Time, int64) (int64, error) {
	return f.monthlyUsage, f.usageErr
}

type fakeFallbackRepository struct {
	tx *fakeFallbackTx
}

func (f *fakeFallbackRepository) RunFallbackTx(ctx context.Context, _ domain.EventKey, fn func(repository.FallbackTx) (bool, error)) (bool, error) {
	return fn(f.tx)
}

func testEvent() domain.CriticalFallbackEvent {
	return domain.CriticalFallbackEvent{
		SiteID: "site-01", AssetID: "batt-01", Sequence: 7,
		Code: "BATTERY_OVERHEAT", TemperatureC: floatPtr(68.2),
		OccurredAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func floatPtr(v float64) *float64 { return &v }

func TestFallbackServiceCreatesAlertAndEventForANewIncident(t *testing.T) {
	tx := &fakeFallbackTx{assetType: "battery_bms", createdAlertID: "alert-01", insertedReturns: true}
	repo := &fakeFallbackRepository{tx: tx}

	created, err := NewFallbackService(repo, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), testEvent(), domain.SMSReceipt{ProviderMessageID: "msg-01"})

	if err != nil {
		t.Fatalf("ReceiveSMS() error = %v", err)
	}
	if !created {
		t.Fatal("created = false, want true for a brand new incident")
	}
	if tx.eventInsertCalls != 1 {
		t.Fatalf("event insert calls = %d, want 1", tx.eventInsertCalls)
	}
	if tx.outboxCalls != 1 {
		t.Fatalf("outbox calls = %d, want 1", tx.outboxCalls)
	}
}

func TestFallbackServiceRejectsCodeForWrongAssetType(t *testing.T) {
	tx := &fakeFallbackTx{assetType: "smart_meter"}
	repo := &fakeFallbackRepository{tx: tx}

	_, err := NewFallbackService(repo, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), testEvent(), domain.SMSReceipt{})

	if !errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("error = %v, want ErrInvalidID for a code/asset mismatch", err)
	}
	if tx.eventInsertCalls != 0 {
		t.Fatalf("event insert calls = %d, want 0", tx.eventInsertCalls)
	}
}

func TestFallbackServicePropagatesAssetLookupFailure(t *testing.T) {
	tx := &fakeFallbackTx{lookupAssetErr: errors.New("database unavailable")}
	_, err := NewFallbackService(&fakeFallbackRepository{tx: tx}, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), testEvent(), domain.SMSReceipt{})
	if err == nil || errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("error = %v, want operational lookup failure", err)
	}
}

func TestFallbackServiceDoesNotCreateAlertWhenLookupFails(t *testing.T) {
	tx := &fakeFallbackTx{assetType: "battery_bms", sourceAlertErr: errors.New("database unavailable")}
	_, err := NewFallbackService(&fakeFallbackRepository{tx: tx}, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), testEvent(), domain.SMSReceipt{})
	if err == nil {
		t.Fatal("error = nil, want alert lookup failure")
	}
	if tx.createdAlertID != "" || tx.eventInsertCalls != 0 {
		t.Fatal("lookup failure must not create a duplicate alert")
	}
}

func TestFallbackServiceReplaysMatchingDuplicateWithoutReopening(t *testing.T) {
	event := testEvent()
	tx := &fakeFallbackTx{
		storedEvent: &domain.StoredFallbackEvent{
			AlertID: "alert-01", SiteID: event.SiteID, AssetID: event.AssetID,
			Code: event.Code, Sequence: event.Sequence,
		},
		insertedReturns: true,
	}
	repo := &fakeFallbackRepository{tx: tx}

	created, err := NewFallbackService(repo, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), event, domain.SMSReceipt{ProviderMessageID: "msg-01"})

	if err != nil {
		t.Fatalf("ReceiveSMS() error = %v", err)
	}
	if created {
		t.Fatal("created = true, want false when replaying a matching duplicate")
	}
	if tx.outboxCalls != 0 {
		t.Fatalf("outbox calls = %d, want 0 for a replay", tx.outboxCalls)
	}
}

func TestFallbackServiceRejectsDuplicateWithMismatchedFields(t *testing.T) {
	event := testEvent()
	tx := &fakeFallbackTx{
		storedEvent: &domain.StoredFallbackEvent{
			AlertID: "alert-01", SiteID: event.SiteID, AssetID: "a-different-asset",
			Code: event.Code, Sequence: event.Sequence,
		},
	}
	repo := &fakeFallbackRepository{tx: tx}

	_, err := NewFallbackService(repo, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), event, domain.SMSReceipt{})

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict for mismatched fields on the same event key", err)
	}
}

func TestFallbackServiceRejectsReceiptWhoseProviderIDBelongsToAnotherEvent(t *testing.T) {
	tx := &fakeFallbackTx{
		assetType: "battery_bms", createdAlertID: "alert-01",
		insertedReturns: false, receiptEventKey: "edge:site-01:999",
	}
	repo := &fakeFallbackRepository{tx: tx}

	_, err := NewFallbackService(repo, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), testEvent(), domain.SMSReceipt{ProviderMessageID: "msg-01"})

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict when the provider message ID belongs to a different event", err)
	}
}

func TestFallbackServiceRejectsConflictingProviderIDForKnownEvent(t *testing.T) {
	event := testEvent()
	tx := &fakeFallbackTx{
		storedEvent: &domain.StoredFallbackEvent{
			AlertID: "alert-01", SiteID: event.SiteID, AssetID: event.AssetID,
			Code: event.Code, Sequence: event.Sequence,
		},
		insertedReturns: false,
		receiptEventKey: "edge:site-01:999",
	}
	repo := &fakeFallbackRepository{tx: tx}

	_, err := NewFallbackService(repo, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), event, domain.SMSReceipt{ProviderMessageID: "msg-01"})

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict when a known event reuses another event's provider ID", err)
	}
}

func TestFallbackServiceFailsClosedWhenReceiptConflictCheckErrors(t *testing.T) {
	tx := &fakeFallbackTx{
		assetType: "battery_bms", createdAlertID: "alert-01",
		insertedReturns: false, findReceiptErr: errors.New("connection reset"),
	}
	repo := &fakeFallbackRepository{tx: tx}

	_, err := NewFallbackService(repo, "alert.opened", domain.SMSPricing{}).
		ReceiveSMS(context.Background(), testEvent(), domain.SMSReceipt{ProviderMessageID: "msg-01"})

	if err == nil {
		t.Fatal("error = nil, want the storage error to propagate rather than be swallowed")
	}
	if errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want the underlying storage error, not a conflict verdict", err)
	}
}

func TestFallbackServiceWarnsNearBudgetWithoutFailing(t *testing.T) {
	tx := &fakeFallbackTx{
		assetType: "battery_bms", createdAlertID: "alert-01",
		insertedReturns: true, monthlyUsage: 900,
	}
	repo := &fakeFallbackRepository{tx: tx}

	_, err := NewFallbackService(repo, "alert.opened", domain.SMSPricing{MonthlyBudgetMinor: 1000}).
		ReceiveSMS(context.Background(), testEvent(), domain.SMSReceipt{ProviderMessageID: "msg-01"})

	if err != nil {
		t.Fatalf("ReceiveSMS() error = %v, want budget threshold to only warn", err)
	}
}

func TestFallbackServicePropagatesBudgetLookupFailure(t *testing.T) {
	tx := &fakeFallbackTx{
		assetType: "battery_bms", createdAlertID: "alert-01", insertedReturns: true,
		usageErr: errors.New("database unavailable"),
	}
	_, err := NewFallbackService(&fakeFallbackRepository{tx: tx}, "alert.opened", domain.SMSPricing{MonthlyBudgetMinor: 1000}).
		ReceiveSMS(context.Background(), testEvent(), domain.SMSReceipt{ProviderMessageID: "msg-01"})
	if err == nil {
		t.Fatal("error = nil, want budget usage failure")
	}
}
