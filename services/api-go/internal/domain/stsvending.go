package domain

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var ErrVendNotSent = errors.New("no vending request was sent; retry the same payment")

var ErrVendInProgress = errors.New("vend is in progress; retry after the active request completes")

var ErrVendNotIssued = errors.New("provider confirmed no token was issued; original payment remains closed to vending")

var ErrVendUncertain = errors.New("vending outcome requires operator reconciliation; do not issue under a new reference")
var vendReference = regexp.MustCompile(`^[a-zA-Z0-9_.,-]{1,40}$`)
var vendMeter = regexp.MustCompile(`^(?:[0-9]{11}|[0-9]{16,18}|[0-9]{35})$`)

// STSVendRequest is an explicit net-of-fees currency purchase, not a kWh amount.
// Reference is globally unique for this platform and the configured vending installation.
type STSVendRequest struct {
	PaymentID   string `json:"payment_id"`
	Reference   string `json:"reference"`
	MeterID     string `json:"meter_id"`
	AmountMinor int64  `json:"amount_minor"`
}

func (r STSVendRequest) Validate() error {
	if !vendReference.MatchString(r.Reference) || !vendMeter.MatchString(r.MeterID) || r.AmountMinor <= 0 {
		return fmt.Errorf("invalid STS reference, registered meter/IDRecord, or currency minor amount")
	}
	return nil
}

// STSVend retains all tokens in delivery order, including key-change tokens.
type STSVend struct {
	Tokens     []string  `json:"tokens"`
	Units      float64   `json:"units"`
	UnitName   string    `json:"unit_name"`
	VendedAt   time.Time `json:"vended_at"`
	IDRecord   string    `json:"id_record"`
	ToIDRecord string    `json:"to_id_record,omitempty"`
}

func (r STSVendRequest) ValidateFunding() error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := validateUUID("payment_id", r.PaymentID); err != nil {
		return err
	}
	if r.Reference != r.PaymentID {
		return fmt.Errorf("STS reference must be its funding payment ID")
	}
	return nil
}
func (v STSVend) Validate() error {
	if len(v.Tokens) == 0 || len(v.Tokens) > 16 || v.VendedAt.IsZero() || v.Units < 0 || math.IsNaN(v.Units) || math.IsInf(v.Units, 0) || (v.UnitName != "kWh" && v.UnitName != "hWh") || !regexp.MustCompile(`^[0-9]{35}$`).MatchString(v.IDRecord) {
		return fmt.Errorf("invalid STS vend")
	}
	for _, token := range v.Tokens {
		if !regexp.MustCompile(`^[0-9]{20}$`).MatchString(token) {
			return fmt.Errorf("invalid STS token")
		}
	}
	return nil
}

type STSVendStatus string

const (
	STSVendOutcomeUncertain     STSVendStatus = "outcome_uncertain"
	STSVendRetryableUnsent      STSVendStatus = "retryable_unsent"
	STSVendRetryBlocked         STSVendStatus = "retry_blocked"
	STSVendNeedsBalanceReversal STSVendStatus = "needs_balance_reversal"
)

type STSVendRecord struct {
	Status STSVendStatus `json:"status"`
	// SafeToRetry proves no request was sent; current funding may still block a retry.
	SafeToRetry          bool      `json:"safe_to_retry"`
	RetryBlockedReason   *string   `json:"retry_blocked_reason,omitempty"`
	LastError            *string   `json:"last_error,omitempty"`
	NeedsBalanceReversal bool      `json:"needs_balance_reversal,omitempty"`
	Reference            string    `json:"reference"`
	PaymentID            *string   `json:"payment_id"`
	MeterID              string    `json:"meter_id"`
	AmountMinor          int64     `json:"amount_minor"`
	RequestedAt          time.Time `json:"requested_at"`
}
type STSReconciliation struct {
	Reference string
	MeterID   string
	Actor     string
	Evidence  string
	Result    *STSVend
	NotIssued bool
}

func (r STSReconciliation) Validate() error {
	if !vendReference.MatchString(r.Reference) || !vendMeter.MatchString(r.MeterID) || strings.TrimSpace(r.Actor) == "" || strings.TrimSpace(r.Evidence) == "" || len(r.Actor) > 200 || len(r.Evidence) > 4000 {
		return fmt.Errorf("reconciliation requires reference, meter, actor and provider evidence")
	}
	if r.NotIssued == (r.Result != nil) {
		return fmt.Errorf("choose recovered tokens or confirmed not issued")
	}
	if r.Result != nil {
		return r.Result.Validate()
	}
	return nil
}

// STSReversalShortfall is a discrepancy to investigate, not customer debt.
type STSReversalShortfall struct {
	Reference           string     `json:"reference"`
	CreditID            string     `json:"credit_id"`
	ShortfallKWh        DecimalKWh `json:"shortfall_kwh"`
	ShortfallMoneyMinor int64      `json:"shortfall_money_minor"`
	ReversedAt          time.Time  `json:"reversed_at"`
}

// DecimalKWh is an exact kWh amount as Postgres numeric text, kept out of
// float64 so reconciliation figures are never rounded.
type DecimalKWh string

var decimalKWh = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

func (k *DecimalKWh) Scan(src any) error {
	var text string
	switch v := src.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("scan kWh: want numeric text, got %T", src)
	}
	if !decimalKWh.MatchString(text) {
		return fmt.Errorf("scan kWh: %q is not a decimal", text)
	}
	*k = DecimalKWh(text)
	return nil
}

func (k DecimalKWh) MarshalJSON() ([]byte, error) {
	if !decimalKWh.MatchString(string(k)) {
		return nil, fmt.Errorf("marshal kWh: %q is not a decimal", string(k))
	}
	return []byte(k), nil
}
