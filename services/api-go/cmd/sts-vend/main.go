// sts-vend issues a currency-valued STS purchase through a provisioned PrismVend
// installation. It prints ordered tokens for manual delivery; it does not mark
// a meter command acknowledged or alter the platform's kWh balances.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"api-go/internal/adapter/prismvend"
	"api-go/internal/domain"
	"api-go/internal/repository/postgres"
	"api-go/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	var request domain.STSVendRequest
	var unresolved, notIssued bool
	var recoverFile, actor, evidence string
	flag.StringVar(&request.Reference, "reference", "", "reservation reference for reconciliation")
	flag.StringVar(&request.PaymentID, "payment", "", "confirmed STS-routed payment ID")
	flag.BoolVar(&unresolved, "list-unresolved", false, "list unresolved reservations without contacting provider")
	flag.StringVar(&recoverFile, "recover", "", "JSON result recovered from provider records")
	flag.BoolVar(&notIssued, "not-issued", false, "record provider-confirmed non-issuance; never retries")
	flag.StringVar(&actor, "actor", "", "operator identity for reconciliation audit")
	flag.StringVar(&evidence, "evidence", "", "provider transaction or support evidence reference")
	flag.StringVar(&request.MeterID, "meter", "", "registered DRN/PAN or blind IDRecord")
	flag.Int64Var(&request.AmountMinor, "amount-minor", 0, "net-of-fees amount in the vending installation's currency hundredths")
	flag.Parse()
	if !unresolved && recoverFile == "" && !notIssued {
		request.Reference = request.PaymentID
		if err := request.ValidateFunding(); err != nil {
			return err
		}
	}
	endpoint := os.Getenv("PRISMVEND_URL")
	installation := os.Getenv("PRISMVEND_INSTALLATION")
	if installation == "" || os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("DATABASE_URL and stable PRISMVEND_INSTALLATION are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewSTSVendStore(pool, installation)
	if unresolved {
		if recoverFile != "" || notIssued {
			return fmt.Errorf("choose one operation")
		}
		records, err := store.ListUnresolved(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(records)
	}
	if recoverFile != "" || notIssued {
		r := domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: actor, Evidence: evidence, NotIssued: notIssued}
		if recoverFile != "" {
			file, err := os.Open(recoverFile)
			if err != nil {
				return err
			}
			defer file.Close()
			decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
			decoder.DisallowUnknownFields()
			var result domain.STSVend
			if err := decoder.Decode(&result); err != nil {
				return err
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return fmt.Errorf("recovery file must contain exactly one JSON result")
			}
			r.Result = &result
		}
		return services.ReconcileSTSVend(ctx, store, r)
	}
	vendor, err := prismvend.New(endpoint)
	if err != nil {
		return err
	}
	service := services.NewSTSVendingService(store, vendor)
	result, err := service.Vend(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
