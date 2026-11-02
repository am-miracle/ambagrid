package domain

import "testing"

func TestSTSReversalUsesExactJointProportions(t *testing.T) {
	for _, tc := range []struct {
		name, credit, balance, wantEnergy string
		money, available, wantMoney       int64
	}{
		{"money cap", "0.200040", "0.200040", "0.100000", 5001, 2500, 2500},
		{"energy cap", "0.200040", "0.100020", "0.100000", 5001, 5001, 2500},
		{"no energy", "0.200040", "0", "0.000000", 5001, 5001, 0},
		{"no money", "0.200040", "0.200040", "0.000000", 5001, 0, 0},
		{"sub minor", "0.200040", "0.000039", "0.000000", 5001, 5001, 0},
		{"sub micro", "0.000001", "0.000001", "0.000000", 5001, 2500, 0},
		{"free legacy credit", "0.200040", "0.1", "0.100000", 0, 1000, 0},
		{"large exact money", "0.200040", "0.100020", "0.100019", 9223372036854775807, 9223372036854775807, 4611686018427387903},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanCreditReversal(CreditValue{DecimalKWh(tc.credit), tc.money}, CreditValue{DecimalKWh(tc.balance), tc.available})
			if err != nil || plan.Reversed.KWh != DecimalKWh(tc.wantEnergy) || plan.Reversed.MoneyMinor != tc.wantMoney {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
		})
	}
}
