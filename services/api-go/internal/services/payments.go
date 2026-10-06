package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"api-go/internal/domain"
)

type PaymentService struct {
	payments PaymentRepository
}

func NewPaymentService(payments PaymentRepository) *PaymentService {
	return &PaymentService{payments: payments}
}

type ApplyDevPaymentRequest struct {
	CustomerID       string
	AmountMinorUnits int64
	Currency         string
}

func (s *PaymentService) ApplyDevPayment(ctx context.Context, request ApplyDevPaymentRequest) (domain.ApplyPaymentResult, error) {
	customerID, err := domain.ValidateCustomerID(request.CustomerID)
	if err != nil {
		return domain.ApplyPaymentResult{}, err
	}

	currency := strings.TrimSpace(request.Currency)
	if currency == "" {
		return domain.ApplyPaymentResult{}, fmt.Errorf("%w: currency must not be empty", ErrInvalidRequest)
	}

	if request.AmountMinorUnits <= 0 {
		return domain.ApplyPaymentResult{}, fmt.Errorf("%w: amount_minor_units must be greater than zero", ErrInvalidRequest)
	}

	return s.applyPayment(ctx, domain.ApplyPaymentCommand{
		Provider:          "dev",
		ExternalReference: newExternalReference(),
		CustomerID:        customerID,
		AmountMinorUnits:  request.AmountMinorUnits,
		Currency:          currency,
		Status:            domain.PaymentStatusConfirmed,
		ConfirmedAt:       time.Now().UTC(),
	})
}

func (s *PaymentService) ListCustomers(ctx context.Context) ([]domain.CustomerSummary, error) {
	return s.payments.ListCustomerSummaries(ctx)
}

func (s *PaymentService) ListMeterCommands(ctx context.Context) ([]domain.MeterCommand, error) {
	return s.payments.ListMeterCommands(ctx)
}

func (s *PaymentService) ListAuditEvents(ctx context.Context, customerID string) ([]domain.AuditEvent, error) {
	return s.payments.ListAuditEvents(ctx, customerID)
}

func (s *PaymentService) ApplyWebhookPayment(ctx context.Context, command domain.ApplyPaymentCommand) (domain.ApplyPaymentResult, error) {
	if err := validatePaymentCommand(command); err != nil {
		return domain.ApplyPaymentResult{}, err
	}
	return s.applyPayment(ctx, command)
}

func (s *PaymentService) applyPayment(ctx context.Context, command domain.ApplyPaymentCommand) (domain.ApplyPaymentResult, error) {
	return s.payments.RunPaymentTx(ctx, func(tx PaymentTx) (domain.ApplyPaymentResult, error) {
		existing, err := tx.FindExistingPayment(ctx, command.Provider, command.ExternalReference)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}
		if existing != nil {
			if err := verifyPaymentConsistency(*existing, command); err != nil {
				return domain.ApplyPaymentResult{}, err
			}
			return tx.ReplayPriorResult(ctx, *existing)
		}

		payment, err := tx.InsertPayment(ctx, command)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}
		if payment == nil {
			// Another transaction won the provider/reference uniqueness race. Under
			// READ COMMITTED, the statement after the blocked INSERT sees that winner.
			existing, err := tx.FindExistingPayment(ctx, command.Provider, command.ExternalReference)
			if err != nil {
				return domain.ApplyPaymentResult{}, err
			}
			if existing == nil {
				return domain.ApplyPaymentResult{}, fmt.Errorf("payment conflict winner was not visible")
			}
			if err := verifyPaymentConsistency(*existing, command); err != nil {
				return domain.ApplyPaymentResult{}, err
			}
			return tx.ReplayPriorResult(ctx, *existing)
		}

		assignment, err := tx.FindActiveAssignment(ctx, command.CustomerID)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		tariff, err := tx.FindActiveTariff(ctx, assignment.SiteID, command.ConfirmedAt)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}
		if tariff.Currency != command.Currency {
			return domain.ApplyPaymentResult{}, fmt.Errorf("%w: payment currency %q does not match tariff currency %q", domain.ErrConflict, command.Currency, tariff.Currency)
		}

		// Tariffs are priced in major currency units while providers report integer
		// minor units, so normalize the denominator before calculating energy.
		pricePerKWhMinor := tariff.PricePerKWh * float64(tariff.MinorUnitsPerMajor)
		kwhGranted := float64(command.AmountMinorUnits) / pricePerKWhMinor

		credit, err := tx.InsertEnergyCredit(ctx, assignment.SiteID, assignment.AssignmentID, payment.PaymentID, tariff.TariffPlanID, kwhGranted, command.AmountMinorUnits)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		priorKWh, err := tx.GetPriorBalance(ctx, assignment.AssignmentID)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		balance, err := tx.UpsertCreditBalance(ctx, assignment.AssignmentID, kwhGranted, command.AmountMinorUnits)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		if err := tx.InsertPaymentConfirmedEvent(ctx, *payment); err != nil {
			return domain.ApplyPaymentResult{}, err
		}
		if err := tx.InsertCreditIssuedEvent(ctx, credit); err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		var meterCommand *domain.MeterCommand
		// Reconnect only on an empty-to-positive transition; later top-ups must not
		// enqueue duplicate commands for a meter that is already being restored.
		needsReconnect := !assignment.RelayClosed && priorKWh == 0 && balance.RemainingKWh > 0
		if needsReconnect {
			cmd, err := tx.InsertMeterCommand(ctx, assignment.MeterID, domain.CommandReconnectMeter, "provider:"+command.Provider, "balance recharged from zero")
			if err != nil {
				return domain.ApplyPaymentResult{}, err
			}
			if err := tx.InsertMeterCommandEvent(ctx, cmd, assignment.SiteID, assignment.AssignmentID); err != nil {
				return domain.ApplyPaymentResult{}, err
			}
			meterCommand = &cmd
		}

		auditMetadata := map[string]any{
			"payment_id":     payment.PaymentID,
			"credit_id":      credit.CreditID,
			"assignment_id":  assignment.AssignmentID,
			"kwh_granted":    kwhGranted,
			"amount":         command.AmountMinorUnits,
			"currency":       command.Currency,
			"tariff_plan_id": tariff.TariffPlanID,
			"remaining_kwh":  balance.RemainingKWh,
		}
		if meterCommand != nil {
			auditMetadata["meter_command_id"] = meterCommand.CommandID
			auditMetadata["meter_command_type"] = meterCommand.CommandType
		}
		if err := tx.InsertAuditEvent(ctx, assignment.SiteID, "provider:"+command.Provider, "payment.applied", "payment", payment.PaymentID, auditMetadata); err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		return domain.ApplyPaymentResult{
			Payment:      *payment,
			Credit:       credit,
			Balance:      balance,
			MeterCommand: meterCommand,
		}, nil
	})
}

func verifyPaymentConsistency(existing domain.Payment, command domain.ApplyPaymentCommand) error {
	if existing.CustomerID != command.CustomerID {
		return fmt.Errorf("%w: duplicate reference %q with different customer_id", domain.ErrConflict, command.ExternalReference)
	}
	if existing.AmountMinorUnits != command.AmountMinorUnits {
		return fmt.Errorf("%w: duplicate reference %q with different amount", domain.ErrConflict, command.ExternalReference)
	}
	if existing.Currency != command.Currency {
		return fmt.Errorf("%w: duplicate reference %q with different currency", domain.ErrConflict, command.ExternalReference)
	}
	if existing.Status != command.Status {
		return fmt.Errorf("%w: duplicate reference %q with different status", domain.ErrConflict, command.ExternalReference)
	}
	return nil
}

func validatePaymentCommand(cmd domain.ApplyPaymentCommand) error {
	if strings.TrimSpace(cmd.Provider) == "" {
		return fmt.Errorf("%w: provider must not be empty", ErrInvalidRequest)
	}
	if strings.TrimSpace(cmd.ExternalReference) == "" {
		return fmt.Errorf("%w: external_reference must not be empty", ErrInvalidRequest)
	}
	if _, err := domain.ValidateCustomerID(cmd.CustomerID); err != nil {
		return err
	}
	if cmd.AmountMinorUnits <= 0 {
		return fmt.Errorf("%w: amount_minor_units must be greater than zero", ErrInvalidRequest)
	}
	if strings.TrimSpace(cmd.Currency) == "" {
		return fmt.Errorf("%w: currency must not be empty", ErrInvalidRequest)
	}
	if cmd.Status != domain.PaymentStatusConfirmed {
		return fmt.Errorf("%w: status must be confirmed", ErrInvalidRequest)
	}
	if cmd.ConfirmedAt.IsZero() {
		return fmt.Errorf("%w: confirmed_at must not be zero", ErrInvalidRequest)
	}
	return nil
}

func newExternalReference() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return "dev-" + hex.EncodeToString(buf[:])
}
