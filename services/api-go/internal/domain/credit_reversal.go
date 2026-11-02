package domain

import (
	"fmt"
	"math/big"
	"strings"
)

// CreditValue keeps energy in exact decimal kWh and money in minor units.
type CreditValue struct {
	KWh        DecimalKWh
	MoneyMinor int64
}

type CreditReversalPlan struct {
	Reversed  CreditValue
	Shortfall CreditValue
}

// PlanCreditReversal reverses the jointly affordable proportion of a funded credit:
// money quantized to whole minor units first, then energy rounded down to micro-kWh.
func PlanCreditReversal(credit, balance CreditValue) (CreditReversalPlan, error) {
	original, err := microKWh(credit.KWh)
	if err != nil {
		return CreditReversalPlan{}, err
	}
	available, err := microKWh(balance.KWh)
	if err != nil {
		return CreditReversalPlan{}, err
	}
	if original.Sign() <= 0 || available.Sign() < 0 || credit.MoneyMinor < 0 || balance.MoneyMinor < 0 {
		return CreditReversalPlan{}, fmt.Errorf("%w: invalid credit or balance", ErrInvalidData)
	}
	reversed := new(big.Int)
	money := int64(0)
	if credit.MoneyMinor == 0 {
		reversed.Set(original)
		if available.Cmp(reversed) < 0 {
			reversed.Set(available)
		}
	} else {
		originalMoney := big.NewInt(credit.MoneyMinor)
		cap := new(big.Int).Quo(new(big.Int).Mul(available, originalMoney), original)
		if cap.Cmp(originalMoney) > 0 {
			cap.Set(originalMoney)
		}
		if cap.Cmp(big.NewInt(balance.MoneyMinor)) > 0 {
			cap.SetInt64(balance.MoneyMinor)
		}
		reversed.Quo(new(big.Int).Mul(original, cap), originalMoney)
		if reversed.Sign() > 0 {
			money = cap.Int64()
		}
	}
	return CreditReversalPlan{
		Reversed:  CreditValue{KWh: decimalFromMicro(reversed), MoneyMinor: money},
		Shortfall: CreditValue{KWh: decimalFromMicro(new(big.Int).Sub(original, reversed)), MoneyMinor: credit.MoneyMinor - money},
	}, nil
}

func microKWh(value DecimalKWh) (*big.Int, error) {
	text := string(value)
	if !decimalKWh.MatchString(text) {
		return nil, fmt.Errorf("%w: invalid kWh decimal", ErrInvalidData)
	}
	whole, fraction, _ := strings.Cut(text, ".")
	// Persistence uses NUMERIC(18,6); reject excess precision rather than rounding.
	if len(fraction) > 6 {
		return nil, fmt.Errorf("%w: kWh exceeds micro-kWh precision", ErrInvalidData)
	}
	negative := strings.HasPrefix(whole, "-")
	whole = strings.TrimPrefix(whole, "-")
	units, ok := new(big.Int).SetString(whole+fraction+strings.Repeat("0", 6-len(fraction)), 10)
	if !ok {
		return nil, fmt.Errorf("%w: invalid kWh decimal", ErrInvalidData)
	}
	if negative {
		units.Neg(units)
	}
	return units, nil
}
func decimalFromMicro(units *big.Int) DecimalKWh {
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(units, big.NewInt(1_000_000), remainder)
	return DecimalKWh(fmt.Sprintf("%s.%06d", whole.String(), remainder.Int64()))
}
