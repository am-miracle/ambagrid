package services

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"api-go/internal/domain"
)

type vendStoreStub struct{ session STSVendSession }

func (s vendStoreStub) Open(context.Context, string) (STSVendSession, error) { return s.session, nil }

type vendSessionStub struct{ completeErr error }

func (s *vendSessionStub) Reserve(context.Context, domain.STSVendRequest) (*domain.STSVend, bool, error) {
	return nil, true, nil
}
func (s *vendSessionStub) Complete(context.Context, string, domain.STSVend) error {
	return s.completeErr
}
func (s *vendSessionStub) RecordUnsent(context.Context, string, string) error { return nil }
func (s *vendSessionStub) Close()                                             {}

type issuedVend struct{ result domain.STSVend }

func (v issuedVend) Vend(context.Context, domain.STSVendRequest) (domain.STSVend, error) {
	return v.result, nil
}

type vendReportStoreStub struct {
	STSVendReportStore
	records []domain.STSVendRecord
	err     error
}

func (s vendReportStoreStub) ListUnresolved(context.Context) ([]domain.STSVendRecord, error) {
	return s.records, s.err
}

func TestSTSVendListingDistinguishesOperatorActions(t *testing.T) {
	store := vendReportStoreStub{records: []domain.STSVendRecord{
		{},
		{SafeToRetry: true},
		{SafeToRetry: true, RetryBlockedReason: new("payment_reversed")},
		{NeedsBalanceReversal: true},
	}}
	records, err := ListUnresolvedSTSVends(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.STSVendStatus{domain.STSVendOutcomeUncertain, domain.STSVendRetryableUnsent, domain.STSVendRetryBlocked, domain.STSVendNeedsBalanceReversal}
	for i, record := range records {
		if record.Status != want[i] {
			t.Errorf("record %d status=%s want=%s", i, record.Status, want[i])
		}
	}
	storageErr := errors.New("database unavailable")
	records, err = ListUnresolvedSTSVends(context.Background(), vendReportStoreStub{err: storageErr})
	if !errors.Is(err, storageErr) || records != nil {
		t.Fatalf("database failure was turned into a funding status: records=%+v err=%v", records, err)
	}
}
func TestSTSVendPreservesIssuedTokensOnPersistenceFailure(t *testing.T) {
	saveErr := errors.New("database unavailable after issuance")
	want := domain.STSVend{Tokens: []string{"11111111111111111111", "22222222222222222222"}, Units: 2, UnitName: "kWh", VendedAt: time.Now().UTC(), IDRecord: "12345678901234567890123456789012345"}
	service := NewSTSVendingService(vendStoreStub{session: &vendSessionStub{completeErr: saveErr}}, issuedVend{result: want})
	id := "00000000-0000-4000-8000-000000000001"
	got, err := service.Vend(context.Background(), domain.STSVendRequest{PaymentID: id, Reference: id, MeterID: "12345678901", AmountMinor: 5001})
	if !errors.Is(err, domain.ErrVendUncertain) || !errors.Is(err, saveErr) {
		t.Fatalf("error=%v, want unresolved persistence failure", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("issued tokens discarded: result=%+v want=%+v", got, want)
	}
}
