package main

import (
	"api-go/internal/domain"
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestIssuedTokensPrintEvenWhenSavingFailed(t *testing.T) {
	var out bytes.Buffer
	saveErr := errors.New("persist vending result failed")
	want := domain.STSVend{Tokens: []string{"11111111111111111111", "22222222222222222222"}}
	err := writeVendResult(&out, want, saveErr)
	if !errors.Is(err, saveErr) {
		t.Fatalf("error=%v, want nonzero failure", err)
	}
	var got domain.STSVend
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("issued result not printed: %q (%v)", out.String(), err)
	}
	if len(got.Tokens) != 2 || got.Tokens[1] != want.Tokens[1] {
		t.Fatalf("tokens=%v", got.Tokens)
	}
}
