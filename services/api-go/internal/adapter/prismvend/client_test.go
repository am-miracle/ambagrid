package prismvend_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"api-go/internal/adapter/prismvend"
	"api-go/internal/domain"
)

func TestVendPreservesOrderedTokensAndCurrencyAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.URL.Path != "/stsvend/VendCredit2.ini" || r.Form.Get("meterId") != "12345678901" || r.Form.Get("value") != "5001" || r.Form.Get("messageId") != "credit-1" || r.Form.Get("subclass") != "0" {
			t.Errorf("wrong vending request: %s %s %v", r.Method, r.URL.Path, r.Form)
		}
		fmt.Fprint(w, "numTokens=2\ntokenDec_1=11111111111111111111\ntokenDec_2=22222222222222222222\nunitsActual=12.5\nunitName=kWh\nvendTimeUnix=1792929600\nidRecord=12345678901234567890123456789012345\n")
	}))
	defer server.Close()
	client, err := prismvend.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Vend(context.Background(), domain.STSVendRequest{Reference: "credit-1", MeterID: "12345678901", AmountMinor: 5001})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tokens) != 2 || result.Tokens[0] != "11111111111111111111" || result.Tokens[1] != "22222222222222222222" || result.Units != 12.5 {
		t.Fatalf("result=%+v", result)
	}
}
func TestVendDoesNotFollowRedirectOrRetryAmbiguousFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Location", "/other")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, _ := prismvend.New(server.URL)
	_, err := client.Vend(context.Background(), domain.STSVendRequest{Reference: "credit-1", MeterID: "12345678901", AmountMinor: 5001})
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestConnectionRefusedIsDefinitelyNotSent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("request reached stopped provider") }))
	endpoint := server.URL
	server.Close()
	client, _ := prismvend.New(endpoint)
	_, err := client.Vend(context.Background(), domain.STSVendRequest{Reference: "credit-1", MeterID: "12345678901", AmountMinor: 5001})
	if !errors.Is(err, domain.ErrVendNotSent) {
		t.Fatalf("connection refusal misclassified: %v", err)
	}
}
func TestLostResponseAfterSendingIsNotSafeToRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer server.Close()
	client, _ := prismvend.New(server.URL)
	_, err := client.Vend(context.Background(), domain.STSVendRequest{Reference: "credit-1", MeterID: "12345678901", AmountMinor: 5001})
	if err == nil || errors.Is(err, domain.ErrVendNotSent) {
		t.Fatalf("sent request was marked retryable: %v", err)
	}
}

func TestVendUsesRecoverableResultValidation(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		valid    bool
	}{
		{"digits", "12345678901234567890123456789012345", true},
		{"letters", "abcdefghijklmnopqrstuvwxyz123456789", false},
		{"short", "12345", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "numTokens=1\ntokenDec_1=11111111111111111111\nunitsActual=12.5\nunitName=kWh\nvendTimeUnix=1792929600\nidRecord=%s\n", tc.id)
			}))
			defer server.Close()
			client, err := prismvend.New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Vend(context.Background(), domain.STSVendRequest{Reference: "credit-1", MeterID: "12345678901", AmountMinor: 5001})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, result=%+v, err=%v", tc.valid, result, err)
			}
			if err == nil {
				recovery := domain.STSReconciliation{Reference: "credit-1", MeterID: "12345678901", Actor: "operator:test", Evidence: "provider verified", Result: &result}
				if err := recovery.Validate(); err != nil {
					t.Fatalf("accepted live result cannot be recovered: %v", err)
				}
			}
		})
	}
}
