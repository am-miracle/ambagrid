// Tests service rules with in-memory repositories.
package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
)

type fakeAlertRepository struct {
	alert             domain.Alert
	getErr            error
	resolveErr        error
	resolutions       []domain.AlertResolution
	gotAlertQuery     domain.AlertQuery
	gotResolveCommand domain.ResolveAlertCommand
	detailCalls       int
	resolveCalls      int
}

func (f *fakeAlertRepository) ListAlerts(_ context.Context, query domain.AlertQuery) (page.Page[domain.Alert], error) {
	f.gotAlertQuery = query
	return page.Page[domain.Alert]{Limit: query.Limit}, nil
}

func (f *fakeAlertRepository) GetAlertWithResolutions(context.Context, string) (domain.Alert, []domain.AlertResolution, error) {
	f.detailCalls++
	return f.alert, f.resolutions, f.getErr
}

func (f *fakeAlertRepository) ResolveAlert(_ context.Context, command domain.ResolveAlertCommand) (domain.Alert, error) {
	f.resolveCalls++
	f.gotResolveCommand = command
	return f.alert, f.resolveErr
}

func testLimits() PageLimits {
	return PageLimits{DefaultSize: 50, MaxSize: 200}
}

const testGlobalHistorySize = 25

func TestSiteHealthStatus(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	recentContact := now.Add(-30 * time.Second)
	currentEvent := now.Add(-45 * time.Second)
	oldContact := now.Add(-3 * time.Minute)
	staleEvent := now.Add(-3 * time.Minute)

	tests := map[string]struct {
		site domain.Site
		want domain.SiteHealthStatus
	}{
		"live": {
			site: domain.Site{LastContactAt: &recentContact, LastEventAt: &currentEvent},
			want: domain.SiteHealthLive,
		},
		"delayed event": {
			site: domain.Site{LastContactAt: &recentContact, LastEventAt: &staleEvent},
			want: domain.SiteHealthDelayed,
		},
		"growing queue": {
			site: domain.Site{LastContactAt: &recentContact, LastEventAt: &currentEvent, QueueGrowing: true},
			want: domain.SiteHealthDelayed,
		},
		"offline": {
			site: domain.Site{LastContactAt: &oldContact, LastEventAt: &currentEvent},
			want: domain.SiteHealthOffline,
		},
		"never contacted": {want: domain.SiteHealthOffline},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := siteHealthStatus(test.site, now, 2*time.Minute, 2*time.Minute); got != test.want {
				t.Fatalf("siteHealthStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

func newTestAlertService(repo AlertRepository) *AlertService {
	return NewAlertService(repo, testLimits(), PageLimits{
		DefaultSize: testGlobalHistorySize,
		MaxSize:     testGlobalHistorySize,
	})
}

func TestPageLimitsResolve(t *testing.T) {
	tests := map[string]struct {
		requested int
		want      int
		wantErr   bool
	}{
		"unset takes the default": {requested: 0, want: 50},
		"in range is honored":     {requested: 10, want: 10},
		"at the ceiling":          {requested: 200, want: 200},
		"over the ceiling":        {requested: 201, wantErr: true},
		"negative":                {requested: -1, wantErr: true},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := testLimits().Resolve(test.requested)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("Resolve() error = %v, want ErrInvalidRequest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Resolve() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestAlertServiceListAppliesTheResolvedLimit(t *testing.T) {
	repo := &fakeAlertRepository{}

	if _, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{}); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if repo.gotAlertQuery.Limit != 50 {
		t.Fatalf("repository saw limit %d, want the default 50", repo.gotAlertQuery.Limit)
	}
}

func TestAlertServiceListDefaultsGlobalAlertsToOpen(t *testing.T) {
	repo := &fakeAlertRepository{}

	if _, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{}); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	status := repo.gotAlertQuery.Filter.Status
	if status == nil || *status != domain.AlertStatusOpen {
		t.Fatalf("status filter = %v, want open for an unscoped global list", status)
	}
}

func TestAlertServiceListCapsUnscopedHistory(t *testing.T) {
	repo := &fakeAlertRepository{}
	resolved := domain.AlertStatusResolved

	_, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{
		Filter: domain.AlertFilter{Status: &resolved},
		Limit:  testGlobalHistorySize + 1,
	})

	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("List() error = %v, want ErrInvalidRequest", err)
	}
}

func TestAlertServiceListDefaultsUnscopedHistoryToTheLowerLimit(t *testing.T) {
	repo := &fakeAlertRepository{}
	resolved := domain.AlertStatusResolved

	if _, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{
		Filter: domain.AlertFilter{Status: &resolved},
	}); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if repo.gotAlertQuery.Limit != testGlobalHistorySize {
		t.Fatalf("repository saw limit %d, want the global history cap", repo.gotAlertQuery.Limit)
	}
}

func TestAlertServiceListLetsScopedHistoryUseTheNormalLimit(t *testing.T) {
	repo := &fakeAlertRepository{}
	resolved := domain.AlertStatusResolved
	siteID := "site-01"

	if _, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{
		Filter: domain.AlertFilter{SiteID: &siteID, Status: &resolved},
		Limit:  50,
	}); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if repo.gotAlertQuery.Limit != 50 {
		t.Fatalf("repository saw limit %d, want 50 for scoped alert history", repo.gotAlertQuery.Limit)
	}
}

func TestAlertServiceGetReturnsResolutionHistoryWithTheAlert(t *testing.T) {
	repo := &fakeAlertRepository{
		alert:       domain.Alert{AlertID: "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b"},
		resolutions: []domain.AlertResolution{{ResolvedBy: "actor-0101"}},
	}

	detail, err := newTestAlertService(repo).Get(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if len(detail.Resolutions) != 1 || detail.Resolutions[0].ResolvedBy != "actor-0101" {
		t.Fatalf("Resolutions = %v, want the repository's history", detail.Resolutions)
	}
}

func TestAlertServiceGetRejectsAMalformedIDBeforeQuerying(t *testing.T) {
	repo := &fakeAlertRepository{getErr: errors.New("should not be called")}

	_, err := newTestAlertService(repo).Get(context.Background(), "not-a-uuid")

	if !errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("Get() error = %v, want ErrInvalidID", err)
	}
	if repo.detailCalls != 0 {
		t.Fatalf("repository was queried %d times, want 0", repo.detailCalls)
	}
}

func TestAlertServiceGetReturnsMissingAlertError(t *testing.T) {
	repo := &fakeAlertRepository{getErr: domain.ErrNotFound}

	if _, err := newTestAlertService(repo).Get(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}

	if repo.detailCalls != 1 {
		t.Fatalf("repository was queried %d times, want 1", repo.detailCalls)
	}
}

func TestAlertServiceResolveValidatesAndPassesACommandThrough(t *testing.T) {
	repo := &fakeAlertRepository{
		alert: domain.Alert{AlertID: "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b", Status: domain.AlertStatusResolved},
	}

	alert, err := newTestAlertService(repo).Resolve(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b", ResolveAlertRequest{
		ResolutionNote: "  fan cleaned  ",
		ResolvedBy:     " actor-0101 ",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if alert.Status != domain.AlertStatusResolved {
		t.Fatalf("alert status = %v, want resolved", alert.Status)
	}
	if got := repo.gotResolveCommand; got.AlertID != "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b" || got.ResolutionNote != "fan cleaned" || got.ResolvedBy != "actor-0101" {
		t.Fatalf("resolve command = %+v", got)
	}
}

func TestAlertServiceResolveRejectsBadInputBeforeQuerying(t *testing.T) {
	tests := map[string]ResolveAlertRequest{
		"empty note":      {ResolutionNote: " ", ResolvedBy: "actor-0101"},
		"empty operator":  {ResolutionNote: "fan cleaned", ResolvedBy: " "},
		"system operator": {ResolutionNote: "fan cleaned", ResolvedBy: "system"},
	}

	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			repo := &fakeAlertRepository{}

			_, err := newTestAlertService(repo).Resolve(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b", request)

			if err == nil {
				t.Fatal("Resolve() error = nil, want validation error")
			}
			if repo.resolveCalls != 0 {
				t.Fatalf("repository was queried %d times, want 0", repo.resolveCalls)
			}
		})
	}
}

type fakePaymentTx struct {
	existingPayment *domain.Payment
	insertedPayment *domain.Payment
	assignment      domain.MeterAssignment
	tariff          domain.Tariff
	priorBalance    float64
	balance         domain.CreditBalance
	credit          domain.EnergyCredit
	meterCommand    domain.MeterCommand
	replayResult    domain.ApplyPaymentResult

	findAssignmentErr error
	findTariffErr     error
	priorBalanceErr   error
	insertCommandErr  error

	insertedCommands []domain.MeterCommand
	auditCalls       int
	outboxCalls      int
}

func (f *fakePaymentTx) FindExistingPayment(context.Context, string, string) (*domain.Payment, error) {
	return f.existingPayment, nil
}

func (f *fakePaymentTx) InsertPayment(context.Context, domain.ApplyPaymentCommand) (*domain.Payment, error) {
	return f.insertedPayment, nil
}

func (f *fakePaymentTx) ReplayPriorResult(context.Context, domain.Payment) (domain.ApplyPaymentResult, error) {
	return f.replayResult, nil
}

func (f *fakePaymentTx) FindActiveAssignment(context.Context, string) (domain.MeterAssignment, error) {
	return f.assignment, f.findAssignmentErr
}

func (f *fakePaymentTx) FindActiveTariff(context.Context, string, time.Time) (domain.Tariff, error) {
	return f.tariff, f.findTariffErr
}

func (f *fakePaymentTx) InsertEnergyCredit(context.Context, string, string, string, string, float64, int64) (domain.EnergyCredit, error) {
	return f.credit, nil
}

func (f *fakePaymentTx) GetPriorBalance(context.Context, string) (float64, error) {
	return f.priorBalance, f.priorBalanceErr
}

func (f *fakePaymentTx) UpsertCreditBalance(context.Context, string, float64, int64) (domain.CreditBalance, error) {
	return f.balance, nil
}

func (f *fakePaymentTx) InsertMeterCommand(_ context.Context, _ string, _ domain.MeterCommandType, _, _ string) (domain.MeterCommand, error) {
	f.insertedCommands = append(f.insertedCommands, f.meterCommand)
	return f.meterCommand, f.insertCommandErr
}

func (f *fakePaymentTx) InsertPaymentConfirmedEvent(context.Context, domain.Payment) error {
	f.outboxCalls++
	return nil
}

func (f *fakePaymentTx) InsertCreditIssuedEvent(context.Context, domain.EnergyCredit) error {
	f.outboxCalls++
	return nil
}

func (f *fakePaymentTx) InsertMeterCommandEvent(context.Context, domain.MeterCommand, string, string) error {
	f.outboxCalls++
	return nil
}

func (f *fakePaymentTx) InsertAuditEvent(context.Context, string, string, string, string, string, map[string]any) error {
	f.auditCalls++
	return nil
}

type fakePaymentRepository struct {
	txCalls    int
	gotCommand domain.ApplyPaymentCommand
	fakeTx     *fakePaymentTx
}

func (f *fakePaymentRepository) RunPaymentTx(_ context.Context, fn func(PaymentTx) (domain.ApplyPaymentResult, error)) (domain.ApplyPaymentResult, error) {
	f.txCalls++
	return fn(f.fakeTx)
}

func (f *fakePaymentRepository) ListCustomerSummaries(_ context.Context) ([]domain.CustomerSummary, error) {
	return nil, nil
}

func (f *fakePaymentRepository) ListMeterCommands(_ context.Context) ([]domain.MeterCommand, error) {
	return nil, nil
}

func (f *fakePaymentRepository) ListAuditEvents(_ context.Context, _ string) ([]domain.AuditEvent, error) {
	return nil, nil
}

func newPaymentTestFixture() *fakePaymentTx {
	confirmedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return &fakePaymentTx{
		insertedPayment: &domain.Payment{
			PaymentID: "pay-001", Provider: "dev", ExternalReference: "dev-abc123",
			CustomerID: "cust-01", AmountMinorUnits: 500000, Currency: "NGN",
			Status: domain.PaymentStatusConfirmed, ConfirmedAt: &confirmedAt,
			CreatedAt: confirmedAt,
		},
		assignment: domain.MeterAssignment{
			AssignmentID: "assign-001", SiteID: "site-01",
			MeterID: "met-0101", RelayClosed: false,
		},
		tariff: domain.Tariff{
			TariffPlanID: "tariff-01", PricePerKWh: 250, Currency: "NGN", MinorUnitsPerMajor: 100,
		},
		credit: domain.EnergyCredit{
			CreditID: "credit-001", SiteID: "site-01", AssignmentID: "assign-001",
		},
		priorBalance: 0,
		balance: domain.CreditBalance{
			AssignmentID: "assign-001", RemainingKWh: 20.0,
			RemainingMoneyValueMinorUnits: 500000,
		},
		meterCommand: domain.MeterCommand{
			CommandID: "cmd-001", MeterID: "met-0101",
			CommandType: domain.CommandReconnectMeter, Status: domain.CommandStatusRequested,
		},
	}
}

func TestPaymentServiceApplyDevPaymentValidatesAndPassesThrough(t *testing.T) {
	fakeTx := newPaymentTestFixture()
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	result, err := NewPaymentService(repo).ApplyDevPayment(context.Background(), ApplyDevPaymentRequest{
		CustomerID:       "  cust-01  ",
		AmountMinorUnits: 500000,
		Currency:         "NGN",
	})
	if err != nil {
		t.Fatalf("ApplyDevPayment() error = %v", err)
	}
	if result.Payment.PaymentID != "pay-001" {
		t.Fatalf("payment_id = %v, want pay-001", result.Payment.PaymentID)
	}
	if repo.txCalls != 1 {
		t.Fatalf("repository calls = %d, want 1", repo.txCalls)
	}
}

func TestPaymentServiceApplyDevPaymentRejectsBadInputBeforeQuerying(t *testing.T) {
	tests := map[string]ApplyDevPaymentRequest{
		"empty customer":  {CustomerID: "  ", AmountMinorUnits: 500000, Currency: "NGN"},
		"zero amount":     {CustomerID: "cust-01", AmountMinorUnits: 0, Currency: "NGN"},
		"negative amount": {CustomerID: "cust-01", AmountMinorUnits: -1, Currency: "NGN"},
		"empty currency":  {CustomerID: "cust-01", AmountMinorUnits: 500000, Currency: ""},
	}

	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			repo := &fakePaymentRepository{fakeTx: newPaymentTestFixture()}

			_, err := NewPaymentService(repo).ApplyDevPayment(context.Background(), request)

			if err == nil {
				t.Fatal("ApplyDevPayment() error = nil, want validation error")
			}
			if repo.txCalls != 0 {
				t.Fatalf("repository was called %d times, want 0", repo.txCalls)
			}
		})
	}
}

func TestPaymentServiceApplyWebhookPaymentValidatesCommand(t *testing.T) {
	confirmedAt := time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)
	tests := map[string]domain.ApplyPaymentCommand{
		"empty provider": {Provider: "", ExternalReference: "ref-1", CustomerID: "cust-01",
			AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt},
		"empty reference": {Provider: "paystack", ExternalReference: "  ", CustomerID: "cust-01",
			AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt},
		"empty customer": {Provider: "paystack", ExternalReference: "ref-1", CustomerID: "",
			AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt},
		"zero amount": {Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
			AmountMinorUnits: 0, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt},
		"negative amount": {Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
			AmountMinorUnits: -100, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt},
		"empty currency": {Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
			AmountMinorUnits: 500000, Currency: "", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt},
		"non-confirmed status": {Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
			AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusPending, ConfirmedAt: confirmedAt},
		"zero confirmed_at": {Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
			AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed},
	}

	for name, command := range tests {
		t.Run(name, func(t *testing.T) {
			repo := &fakePaymentRepository{fakeTx: newPaymentTestFixture()}

			_, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), command)

			if err == nil {
				t.Fatal("ApplyWebhookPayment() error = nil, want validation error")
			}
			if repo.txCalls != 0 {
				t.Fatalf("repository was called %d times, want 0", repo.txCalls)
			}
		})
	}
}

func TestPaymentServiceApplyWebhookPaymentPassesValidCommandThrough(t *testing.T) {
	fakeTx := newPaymentTestFixture()
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	result, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider:          "paystack",
		ExternalReference: "ps-ref-123",
		CustomerID:        "cust-01",
		AmountMinorUnits:  500000,
		Currency:          "NGN",
		Status:            domain.PaymentStatusConfirmed,
		ConfirmedAt:       time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ApplyWebhookPayment() error = %v", err)
	}
	if result.Payment.PaymentID != "pay-001" {
		t.Fatalf("payment_id = %v, want pay-001", result.Payment.PaymentID)
	}
	if repo.txCalls != 1 {
		t.Fatalf("repository calls = %d, want 1", repo.txCalls)
	}
}

func TestPaymentServiceCalculatesKWhFromTariff(t *testing.T) {
	fakeTx := newPaymentTestFixture()
	fakeTx.tariff = domain.Tariff{TariffPlanID: "tariff-01", PricePerKWh: 250, Currency: "NGN", MinorUnitsPerMajor: 100}
	fakeTx.balance = domain.CreditBalance{AssignmentID: "assign-001", RemainingKWh: 20.0, RemainingMoneyValueMinorUnits: 500000}
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	result, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed,
		ConfirmedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result.Balance.RemainingKWh != 20.0 {
		t.Fatalf("remaining_kwh = %v, want 20.0 (500000 / (250 * 100))", result.Balance.RemainingKWh)
	}
}

func TestPaymentServiceRejectsTariffCurrencyMismatch(t *testing.T) {
	fakeTx := newPaymentTestFixture()
	fakeTx.tariff.Currency = "KES"
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	_, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed,
		ConfirmedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict for tariff currency mismatch", err)
	}
}

func TestPaymentServiceIssuesReconnectWhenBalanceGoesFromZeroToPositive(t *testing.T) {
	fakeTx := newPaymentTestFixture()
	fakeTx.assignment.RelayClosed = false
	fakeTx.priorBalance = 0
	fakeTx.balance.RemainingKWh = 20.0
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	result, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed,
		ConfirmedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result.MeterCommand == nil || result.MeterCommand.CommandType != domain.CommandReconnectMeter {
		t.Fatalf("meter_command = %+v, want reconnect_meter", result.MeterCommand)
	}
}

func TestPaymentServiceSkipsReconnectWhenBalanceWasPositive(t *testing.T) {
	fakeTx := newPaymentTestFixture()
	fakeTx.assignment.RelayClosed = false
	fakeTx.priorBalance = 5.0
	fakeTx.balance.RemainingKWh = 25.0
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	result, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed,
		ConfirmedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result.MeterCommand != nil {
		t.Fatalf("meter_command = %+v, want nil when balance was already positive", result.MeterCommand)
	}
}

func TestPaymentServiceSkipsReconnectWhenRelayIsClosed(t *testing.T) {
	fakeTx := newPaymentTestFixture()
	fakeTx.assignment.RelayClosed = true
	fakeTx.priorBalance = 0
	fakeTx.balance.RemainingKWh = 20.0
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	result, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed,
		ConfirmedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result.MeterCommand != nil {
		t.Fatalf("meter_command = %+v, want nil when relay is already closed", result.MeterCommand)
	}
}

func TestPaymentServiceReplaysDuplicatePayment(t *testing.T) {
	confirmedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fakeTx := newPaymentTestFixture()
	existing := domain.Payment{
		PaymentID: "pay-existing", Provider: "paystack", ExternalReference: "ref-1",
		CustomerID: "cust-01", AmountMinorUnits: 500000, Currency: "NGN",
		Status: domain.PaymentStatusConfirmed, ConfirmedAt: &confirmedAt,
	}
	fakeTx.existingPayment = &existing
	fakeTx.insertedPayment = nil
	fakeTx.replayResult = domain.ApplyPaymentResult{
		Payment: existing,
		Credit:  domain.EnergyCredit{CreditID: "credit-replayed"},
	}
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	result, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt,
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result.Payment.PaymentID != "pay-existing" {
		t.Fatalf("payment_id = %v, want pay-existing (replayed)", result.Payment.PaymentID)
	}
	if result.Credit.CreditID != "credit-replayed" {
		t.Fatalf("credit_id = %v, want credit-replayed", result.Credit.CreditID)
	}
}

func TestPaymentServiceRejectsDuplicateWithDifferentAmount(t *testing.T) {
	confirmedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fakeTx := newPaymentTestFixture()
	existing := domain.Payment{
		PaymentID: "pay-existing", Provider: "paystack", ExternalReference: "ref-1",
		CustomerID: "cust-01", AmountMinorUnits: 999999, Currency: "NGN",
		Status: domain.PaymentStatusConfirmed, ConfirmedAt: &confirmedAt,
	}
	fakeTx.existingPayment = &existing
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	_, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict for inconsistent amount", err)
	}
}

func TestPaymentServiceRejectsDuplicateWithDifferentStatus(t *testing.T) {
	confirmedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fakeTx := newPaymentTestFixture()
	existing := domain.Payment{
		PaymentID: "pay-existing", Provider: "paystack", ExternalReference: "ref-1",
		CustomerID: "cust-01", AmountMinorUnits: 500000, Currency: "NGN",
		Status: domain.PaymentStatusPending,
	}
	fakeTx.existingPayment = &existing
	repo := &fakePaymentRepository{fakeTx: fakeTx}

	_, err := NewPaymentService(repo).ApplyWebhookPayment(context.Background(), domain.ApplyPaymentCommand{
		Provider: "paystack", ExternalReference: "ref-1", CustomerID: "cust-01",
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: confirmedAt,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict for inconsistent status", err)
	}
}

type fakeAssetRepository struct {
	gotQuery     domain.AssetQuery
	calls        int
	asset        domain.Asset
	getErr       error
	readingQuery domain.ReadingQuery
	readingCalls int
}

func (f *fakeAssetRepository) ListAssets(_ context.Context, query domain.AssetQuery) (page.Page[domain.Asset], error) {
	f.gotQuery = query
	return page.Page[domain.Asset]{Limit: query.Limit}, nil
}

func (f *fakeAssetRepository) GetAsset(context.Context, string) (domain.Asset, error) {
	f.calls++
	return f.asset, f.getErr
}

func (f *fakeAssetRepository) GetReadingSeries(_ context.Context, query domain.ReadingQuery) (domain.ReadingSeries, error) {
	f.readingCalls++
	f.readingQuery = query
	return domain.ReadingSeries{}, nil
}

func TestAssetServiceGetRejectsAnEmptyID(t *testing.T) {
	repo := &fakeAssetRepository{}

	_, err := NewAssetService(repo, testLimits()).Get(context.Background(), "   ")

	if !errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("Get() error = %v, want ErrInvalidID", err)
	}
	if repo.calls != 0 {
		t.Fatalf("repository was queried %d times, want 0", repo.calls)
	}
}

func TestAssetServiceListPassesFiltersThrough(t *testing.T) {
	repo := &fakeAssetRepository{}
	siteID := "site-01"
	assetType := domain.AssetTypeBatteryBMS

	_, err := NewAssetService(repo, testLimits()).List(context.Background(), ListAssetsRequest{
		Filter: domain.AssetFilter{SiteID: &siteID, AssetType: &assetType},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if got := repo.gotQuery.Filter; got.SiteID == nil || *got.SiteID != siteID || got.AssetType == nil || *got.AssetType != assetType {
		t.Fatalf("repository saw filter %+v, want the requested one", got)
	}
}

func TestAssetServiceReadingsPassesABoundedSeriesQueryThrough(t *testing.T) {
	repo := &fakeAssetRepository{asset: domain.Asset{AssetType: domain.AssetTypeSmartMeter}}
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	_, err := NewAssetService(repo, testLimits()).Readings(context.Background(), "met-0104", GetReadingSeriesRequest{
		From:     from,
		To:       to,
		Metric:   domain.ReadingMetricVoltage,
		Interval: domain.ReadingIntervalOneMinute,
	})
	if err != nil {
		t.Fatalf("Readings() error = %v", err)
	}
	if repo.readingCalls != 1 || repo.readingQuery.AssetID != "met-0104" || repo.readingQuery.AssetType != domain.AssetTypeSmartMeter || repo.readingQuery.From != from || repo.readingQuery.To != to {
		t.Fatalf("repository query = %+v", repo.readingQuery)
	}
}

func TestAssetServiceReadingsRejectsInvalidWindowsBeforeQuerying(t *testing.T) {
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	tests := map[string]time.Time{
		"equal endpoints":    from,
		"reversed endpoints": from.Add(-time.Minute),
		"window over limit":  from.Add(24*time.Hour + time.Nanosecond),
	}

	for name, to := range tests {
		t.Run(name, func(t *testing.T) {
			repo := &fakeAssetRepository{}
			_, err := NewAssetService(repo, testLimits()).Readings(context.Background(), "met-0104", GetReadingSeriesRequest{
				From:     from,
				To:       to,
				Metric:   domain.ReadingMetricVoltage,
				Interval: domain.ReadingIntervalOneMinute,
			})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Readings() error = %v, want ErrInvalidRequest", err)
			}
			if repo.calls != 0 || repo.readingCalls != 0 {
				t.Fatalf("repository calls = asset:%d readings:%d, want none", repo.calls, repo.readingCalls)
			}
		})
	}
}

func TestAssetServiceReadingsRejectsMetricOutsideTheAssetSchema(t *testing.T) {
	repo := &fakeAssetRepository{asset: domain.Asset{AssetType: domain.AssetTypeBatteryBMS}}
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	_, err := NewAssetService(repo, testLimits()).Readings(context.Background(), "bat-0104", GetReadingSeriesRequest{
		From:     from,
		To:       from.Add(time.Hour),
		Metric:   domain.ReadingMetricVoltage,
		Interval: domain.ReadingIntervalFiveMinutes,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Readings() error = %v, want ErrInvalidRequest", err)
	}
	if repo.calls != 1 || repo.readingCalls != 0 {
		t.Fatalf("repository calls = asset:%d readings:%d, want 1/0", repo.calls, repo.readingCalls)
	}
}
