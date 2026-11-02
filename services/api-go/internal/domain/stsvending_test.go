package domain

import (
	"encoding/json"
	"testing"
)

func TestDecimalKWhKeepsExactDigitsAsAJSONNumber(t *testing.T) {
	var k DecimalKWh
	if err := k.Scan("0.100040"); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(struct {
		Shortfall DecimalKWh `json:"shortfall_kwh"`
	}{k})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"shortfall_kwh":0.100040}` {
		t.Fatalf("json = %s, want the exact digits as a number", out)
	}
	for _, bad := range []any{"1e3", "NaN", "", "0.1; DROP", 3.5} {
		if err := new(DecimalKWh).Scan(bad); err == nil {
			t.Errorf("Scan(%#v) accepted a non-decimal", bad)
		}
	}
}
