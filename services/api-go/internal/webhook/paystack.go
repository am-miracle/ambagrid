package webhook

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"api-go/internal/domain"
)

type Paystack struct {
	secretKey []byte
}

func NewPaystack(secretKey string) (*Paystack, error) {
	if secretKey == "" {
		return nil, fmt.Errorf("paystack secret key must not be empty")
	}
	return &Paystack{secretKey: []byte(secretKey)}, nil
}

func (p *Paystack) Name() string { return "paystack" }

func (p *Paystack) VerifySignature(body []byte, signature string) error {
	mac := hmac.New(sha512.New, p.secretKey)
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return fmt.Errorf("invalid paystack signature")
	}
	return nil
}

func (p *Paystack) SignatureHeader() string { return "X-Paystack-Signature" }

func (p *Paystack) ParsePayment(body []byte) (domain.ApplyPaymentCommand, error) {
	var envelope paystackEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return domain.ApplyPaymentCommand{}, fmt.Errorf("parse paystack webhook: %w", err)
	}

	if envelope.Event != "charge.success" {
		return domain.ApplyPaymentCommand{}, fmt.Errorf("unhandled paystack event: %q", envelope.Event)
	}
	if envelope.Data.Status != "success" {
		return domain.ApplyPaymentCommand{}, fmt.Errorf("paystack charge status is %q, not success", envelope.Data.Status)
	}
	if envelope.Data.Reference == "" {
		return domain.ApplyPaymentCommand{}, fmt.Errorf("paystack webhook missing reference")
	}

	customerID := envelope.Data.Metadata.CustomerID
	if customerID == "" {
		return domain.ApplyPaymentCommand{}, fmt.Errorf("paystack webhook missing metadata.customer_id")
	}
	if envelope.Data.Amount <= 0 {
		return domain.ApplyPaymentCommand{}, fmt.Errorf("paystack webhook amount must be positive")
	}
	if envelope.Data.Currency == "" {
		return domain.ApplyPaymentCommand{}, fmt.Errorf("paystack webhook missing currency")
	}
	if envelope.Data.PaidAt.IsZero() {
		return domain.ApplyPaymentCommand{}, fmt.Errorf("paystack webhook missing paid_at")
	}

	return domain.ApplyPaymentCommand{
		Provider:          p.Name(),
		ExternalReference: envelope.Data.Reference,
		CustomerID:        customerID,
		AmountMinorUnits:  envelope.Data.Amount,
		Currency:          envelope.Data.Currency,
		Status:            domain.PaymentStatusConfirmed,
		ConfirmedAt:       envelope.Data.PaidAt,
	}, nil
}

type paystackEnvelope struct {
	Event string       `json:"event"`
	Data  paystackData `json:"data"`
}

type paystackData struct {
	Reference string           `json:"reference"`
	Amount    int64            `json:"amount"`
	Currency  string           `json:"currency"`
	Status    string           `json:"status"`
	PaidAt    time.Time        `json:"paid_at"`
	Metadata  paystackMetadata `json:"metadata"`
}

type paystackMetadata struct {
	CustomerID string `json:"customer_id"`
}
