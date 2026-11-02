package postgres

import (
	"api-go/internal/domain"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

// This is the pre-refactor NUMERIC calculation, retained only as an independent
// database oracle for the domain calculation. It never mutates financial rows.
func TestCreditReversalPlanMatchesPreviousPostgresArithmetic(t *testing.T) {
	pool := integrationPool(t)
	rng := rand.New(rand.NewPCG(19, 43))
	for i := 0; i < 80; i++ {
		units, available := rng.Int64N(999_999_999_999_999_999)+1, rng.Int64N(999_999_999_999_999_999)
		money, balanceMoney := rng.Int64N(math.MaxInt64), rng.Int64N(math.MaxInt64)
		if i%9 == 0 {
			money = 0
		}
		if i%11 == 0 {
			available = 0
		}
		if i%13 == 0 {
			balanceMoney = 0
		}
		creditKWh := fmt.Sprintf("%d.%06d", units/1_000_000, units%1_000_000)
		balanceKWh := fmt.Sprintf("%d.%06d", available/1_000_000, available%1_000_000)
		var wantKWh string
		var wantMoney int64
		err := pool.QueryRow(context.Background(), `WITH capped AS (
   SELECT CASE WHEN $2::bigint=0 THEN 0::numeric ELSE LEAST($2::numeric,$4::numeric,div($3::numeric*$2::numeric,$1::numeric)) END AS money
  ),paired AS (
   SELECT (CASE WHEN $2::bigint=0 THEN LEAST($3::numeric,$1::numeric)
   ELSE div($1::numeric*1000000*money,$2::numeric)/1000000 END)::numeric(18,6) AS kwh,money FROM capped
  ) SELECT kwh::text,(CASE WHEN kwh=0 THEN 0 ELSE money END)::bigint FROM paired`, creditKWh, money, balanceKWh, balanceMoney).Scan(&wantKWh, &wantMoney)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := domain.PlanCreditReversal(domain.CreditValue{KWh: domain.DecimalKWh(creditKWh), MoneyMinor: money}, domain.CreditValue{KWh: domain.DecimalKWh(balanceKWh), MoneyMinor: balanceMoney})
		if err != nil || string(plan.Reversed.KWh) != wantKWh || plan.Reversed.MoneyMinor != wantMoney {
			t.Fatalf("case %d: credit=%s/%d balance=%s/%d plan=%+v want=%s/%d err=%v", i, creditKWh, money, balanceKWh, balanceMoney, plan, wantKWh, wantMoney, err)
		}
	}
}
