package domain

import (
	"errors"
	"testing"
	"time"
)

func TestSTSReconciliationPreservesTerminalDecisions(t *testing.T) {
	request := STSReconciliation{Reference: "payment-1", MeterID: "12345678901", Actor: "operator", Evidence: "provider evidence", NotIssued: true}
	for _, tc := range []struct {
		name      string
		state     STSReservationState
		recovered bool
		blocked   bool
		reverse   bool
	}{
		{"funded non-issuance", STSReservationState{MeterID: request.MeterID, Funded: true}, false, false, true},
		{"unfunded legacy non-issuance", STSReservationState{MeterID: request.MeterID}, false, false, false},
		{"repair legacy reversal", STSReservationState{MeterID: request.MeterID, Funded: true, NotIssued: true}, false, false, true},
		{"duplicate reversal", STSReservationState{MeterID: request.MeterID, Funded: true, NotIssued: true, CreditReversed: true}, false, true, false},
		{"closed unfunded vend", STSReservationState{MeterID: request.MeterID, NotIssued: true}, false, true, false},
		{"completed vend", STSReservationState{MeterID: request.MeterID, Completed: true}, false, true, false},
		{"different meter", STSReservationState{MeterID: "99999999999"}, false, true, false},
		{"recovered tokens", STSReservationState{MeterID: request.MeterID, Funded: true}, true, false, false},
		{"recover after non-issuance", STSReservationState{MeterID: request.MeterID, Funded: true, NotIssued: true}, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request
			if tc.recovered {
				r.NotIssued = false
				r.Result = &STSVend{Tokens: []string{"12345678901234567890"}, UnitName: "kWh", VendedAt: time.Now(), IDRecord: "12345678901234567890123456789012345"}
			}
			plan, err := tc.state.Reconcile(r)
			if tc.blocked {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("want conflict, got %v", err)
				}
				return
			}
			if err != nil || plan.ReverseCredit != tc.reverse {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			if tc.recovered && plan.Outcome != "recovered" {
				t.Fatalf("wrong recovery: %+v", plan)
			}
		})
	}
}
