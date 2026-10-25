package domain

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

var ErrVendUncertain = errors.New("vending outcome requires operator reconciliation; do not issue under a new reference")
var vendReference = regexp.MustCompile(`^[a-zA-Z0-9_.,-]{1,40}$`)
var vendMeter = regexp.MustCompile(`^(?:[0-9]{11}|[0-9]{16,18}|[0-9]{35})$`)

// STSVendRequest is an explicit net-of-fees currency purchase, not a kWh amount.
// Reference is globally unique for this platform and the configured vending installation.
type STSVendRequest struct {
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
