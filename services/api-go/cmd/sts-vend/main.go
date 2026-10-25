// sts-vend issues a currency-valued STS purchase through a provisioned PrismVend
// installation. It prints ordered tokens for manual delivery; it does not mark
// a meter command acknowledged or alter the platform's kWh balances.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
	flag.StringVar(&request.Reference, "reference", "", "stable purchase reference; never change after an uncertain result")
	flag.StringVar(&request.MeterID, "meter", "", "registered DRN/PAN or blind IDRecord")
	flag.Int64Var(&request.AmountMinor, "amount-minor", 0, "net-of-fees amount in the vending installation's currency hundredths")
	flag.Parse()
	if err := request.Validate(); err != nil {
		return err
	}
	endpoint := os.Getenv("PRISMVEND_URL")
	installation := os.Getenv("PRISMVEND_INSTALLATION")
	if installation == "" || os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("DATABASE_URL and stable PRISMVEND_INSTALLATION are required")
	}
	vendor, err := prismvend.New(endpoint)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	service := services.NewSTSVendingService(postgres.NewSTSVendStore(pool, installation), vendor)
	result, err := service.Vend(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
