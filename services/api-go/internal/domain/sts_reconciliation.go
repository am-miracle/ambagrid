package domain

import "fmt"

// STSReservationState is the locked reservation snapshot needed to decide an
// operator reconciliation. It contains no persistence or money arithmetic.
type STSReservationState struct {
	MeterID        string
	Completed      bool
	NotIssued      bool
	Funded         bool
	CreditReversed bool
}

const (
	STSOutcomeRecovered = "recovered"
	STSOutcomeNotIssued = "not_issued"
)

type STSReconciliationPlan struct {
	Outcome       string
	ReverseCredit bool
}

// Reconcile preserves terminal decisions. A legacy non-issuance may only be
// repaired when its funded credit reversal is still missing.
func (s STSReservationState) Reconcile(r STSReconciliation) (STSReconciliationPlan, error) {
	if err := r.Validate(); err != nil {
		return STSReconciliationPlan{}, err
	}
	if s.MeterID != r.MeterID || s.Completed {
		return STSReconciliationPlan{}, fmt.Errorf("%w: reservation already resolved or meter differs", ErrConflict)
	}
	if s.NotIssued && (!r.NotIssued || !s.Funded || s.CreditReversed) {
		return STSReconciliationPlan{}, fmt.Errorf("%w: non-issuance already resolved", ErrConflict)
	}
	if r.Result != nil {
		return STSReconciliationPlan{Outcome: STSOutcomeRecovered}, nil
	}
	return STSReconciliationPlan{Outcome: STSOutcomeNotIssued, ReverseCredit: s.Funded}, nil
}
