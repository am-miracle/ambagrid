package webhook

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"api-go/internal/domain"
)

const testSecret = "sk_test_xxxxxxxxxxxxxxxxxxxxx"

func sign(body []byte) string {
	mac := hmac.New(sha512.New, []byte(testSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

var testPaidAt = time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)

func chargeSuccessBody(reference, customerID, currency, status string, amount int64) []byte {
	envelope := map[string]any{
		"event": "charge.success",
		"data": map[string]any{
			"reference": reference,
			"amount":    amount,
			"currency":  currency,
			"status":    status,
			"paid_at":   testPaidAt.Format(time.RFC3339),
			"metadata": map[string]any{
				"customer_id": customerID,
			},
		},
	}
	body, _ := json.Marshal(envelope)
	return body
}

func newTestPaystack() *Paystack {
	p, _ := NewPaystack(testSecret)
	return p
}

func TestPaystackNewRejectsEmptySecret(t *testing.T) {
	_, err := NewPaystack("")
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestPaystackVerifySignatureAcceptsValidSignature(t *testing.T) {
	p := newTestPaystack()
	body := chargeSuccessBody("ref-001", "cust-01", "NGN", "success", 500000)

	if err := p.VerifySignature(body, sign(body)); err != nil {
		t.Fatalf("VerifySignature() error = %v", err)
	}
}

func TestPaystackVerifySignatureRejectsInvalidSignature(t *testing.T) {
	p := newTestPaystack()
	body := chargeSuccessBody("ref-001", "cust-01", "NGN", "success", 500000)

	err := p.VerifySignature(body, "deadbeef")
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestPaystackVerifySignatureRejectsTamperedBody(t *testing.T) {
	p := newTestPaystack()
	body := chargeSuccessBody("ref-001", "cust-01", "NGN", "success", 500000)
	sig := sign(body)

	tampered := chargeSuccessBody("ref-001", "cust-01", "NGN", "success", 999999)
	err := p.VerifySignature(tampered, sig)
	if err == nil {
		t.Fatal("expected error for tampered body")
	}
}

func TestPaystackParsePaymentExtractsFields(t *testing.T) {
	p := newTestPaystack()
	body := chargeSuccessBody("txn-abc-123", "cust-42", "NGN", "success", 750000)

	cmd, err := p.ParsePayment(body)
	if err != nil {
		t.Fatalf("ParsePayment() error = %v", err)
	}

	if cmd.Provider != "paystack" {
		t.Errorf("Provider = %q, want paystack", cmd.Provider)
	}
	if cmd.ExternalReference != "txn-abc-123" {
		t.Errorf("ExternalReference = %q, want txn-abc-123", cmd.ExternalReference)
	}
	if cmd.CustomerID != "cust-42" {
		t.Errorf("CustomerID = %q, want cust-42", cmd.CustomerID)
	}
	if cmd.AmountMinorUnits != 750000 {
		t.Errorf("AmountMinorUnits = %d, want 750000", cmd.AmountMinorUnits)
	}
	if cmd.Currency != "NGN" {
		t.Errorf("Currency = %q, want NGN", cmd.Currency)
	}
	if cmd.Status != domain.PaymentStatusConfirmed {
		t.Errorf("Status = %q, want confirmed", cmd.Status)
	}
	if !cmd.ConfirmedAt.Equal(testPaidAt) {
		t.Errorf("ConfirmedAt = %v, want %v", cmd.ConfirmedAt, testPaidAt)
	}
}

func TestPaystackParsePaymentRejectsNonChargeSuccess(t *testing.T) {
	p := newTestPaystack()
	body := []byte(`{"event": "transfer.success", "data": {"reference": "ref-001", "amount": 500000, "currency": "NGN", "status": "success", "paid_at": "2026-10-01T14:30:00Z", "metadata": {"customer_id": "cust-01"}}}`)

	_, err := p.ParsePayment(body)
	if err == nil {
		t.Fatal("expected error for non-charge.success event")
	}
	if !strings.Contains(err.Error(), "unhandled paystack event") {
		t.Fatalf("error = %v, want unhandled event message", err)
	}
}

func TestPaystackParsePaymentRejectsNonSuccessStatus(t *testing.T) {
	p := newTestPaystack()
	body := chargeSuccessBody("ref-001", "cust-01", "NGN", "failed", 500000)
	// Override event to charge.success but status to failed
	_, err := p.ParsePayment(body)
	if err == nil {
		t.Fatal("expected error for non-success status")
	}
}

func bodyWithoutPaidAt() []byte {
	envelope := map[string]any{
		"event": "charge.success",
		"data": map[string]any{
			"reference": "ref-001",
			"amount":    500000,
			"currency":  "NGN",
			"status":    "success",
			"metadata":  map[string]any{"customer_id": "cust-01"},
		},
	}
	body, _ := json.Marshal(envelope)
	return body
}

func TestPaystackParsePaymentRejectsMissingFields(t *testing.T) {
	tests := map[string][]byte{
		"missing reference":   chargeSuccessBody("", "cust-01", "NGN", "success", 500000),
		"missing customer_id": chargeSuccessBody("ref-001", "", "NGN", "success", 500000),
		"missing currency":    chargeSuccessBody("ref-001", "cust-01", "", "success", 500000),
		"zero amount":         chargeSuccessBody("ref-001", "cust-01", "NGN", "success", 0),
		"negative amount":     chargeSuccessBody("ref-001", "cust-01", "NGN", "success", -100),
		"missing paid_at":     bodyWithoutPaidAt(),
	}

	p := newTestPaystack()
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := p.ParsePayment(body)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPaystackParsePaymentRejectsInvalidJSON(t *testing.T) {
	p := newTestPaystack()
	_, err := p.ParsePayment([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestPaystackName(t *testing.T) {
	p := newTestPaystack()
	if p.Name() != "paystack" {
		t.Fatalf("Name() = %q, want paystack", p.Name())
	}
}

func TestPaystackSignatureHeader(t *testing.T) {
	p := newTestPaystack()
	if got := p.SignatureHeader(); got != "X-Paystack-Signature" {
		t.Fatalf("SignatureHeader() = %q, want X-Paystack-Signature", got)
	}
}
