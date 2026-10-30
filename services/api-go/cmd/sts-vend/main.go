// sts-vend issues funded STS purchases for manual token delivery and reconciles
// outcomes; confirmed non-issuance reverses the associated platform credit.
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	var unresolved, shortfalls, unvended, notIssued bool
	var recoverFile, actor, evidence string
	flag.StringVar(&request.Reference, "reference", "", "reservation reference for reconciliation")
	flag.StringVar(&request.PaymentID, "payment", "", "confirmed STS-routed payment ID")
	flag.BoolVar(&unresolved, "list-unresolved", false, "list incomplete reservations and retry eligibility without contacting provider")
	flag.BoolVar(&shortfalls, "list-shortfalls", false, "list non-issuance reversals the balance could not fully cover")
	flag.BoolVar(&unvended, "list-unvended", false, "list funded STS credits with no vend reserved")
	flag.StringVar(&recoverFile, "recover", "", "JSON result recovered from provider records")
	flag.BoolVar(&notIssued, "not-issued", false, "record provider non-issuance and reverse its funded credit; never retries")
	flag.StringVar(&actor, "actor", "", "operator identity for the vend or reconciliation audit")
	flag.StringVar(&evidence, "evidence", "", "provider transaction or support evidence reference")
	flag.StringVar(&request.MeterID, "meter", "", "registered DRN/PAN or blind IDRecord")
	flag.Int64Var(&request.AmountMinor, "amount-minor", 0, "net-of-fees amount in the vending installation's currency hundredths")
	flag.Parse()
	if !unresolved && !shortfalls && !unvended && recoverFile == "" && !notIssued {
		request.Reference = request.PaymentID
		request.RequestedBy = actor
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
		return fmt.Errorf("open database pool: %w", err)
	}
	defer pool.Close()
	store := postgres.NewSTSVendStore(pool, installation)
	modes := 0
	for _, selected := range []bool{unresolved, shortfalls, unvended, recoverFile != "" || notIssued} {
		if selected {
			modes++
		}
	}
	if modes > 1 {
		return fmt.Errorf("choose one operation")
	}
	if shortfalls {
		records, err := services.ListSTSReversalShortfalls(ctx, store)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(records)
	}
	if unvended {
		records, err := services.ListUnvendedSTSCredits(ctx, store)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(records)
	}
	if unresolved {
		records, err := services.ListUnresolvedSTSVends(ctx, store)
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
				return fmt.Errorf("open recovery file: %w", err)
			}
			defer file.Close()
			decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
			decoder.DisallowUnknownFields()
			var result domain.STSVend
			if err := decoder.Decode(&result); err != nil {
				return fmt.Errorf("decode recovery file: %w", err)
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
	return writeVendResult(os.Stdout, result, err)
}

// Issued tokens must reach stdout even when database persistence failed. The
// nonzero exit/error still tells the operator to save and reconcile that result.
func writeVendResult(out io.Writer, result domain.STSVend, vendErr error) error {
	if vendErr != nil && len(result.Tokens) == 0 {
		return vendErr
	}
	return errors.Join(vendErr, json.NewEncoder(out).Encode(result))
}
