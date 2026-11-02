package controller

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"api-go/internal/domain"
)

type BacklogSource interface {
	Backlog(context.Context) (domain.ReconciliationBacklog, error)
}

// MetricsHandler reads the backlog per scrape. A failed read fails the scrape, so
// an unreachable database is not mistaken for an empty queue.
func MetricsHandler(source BacklogSource, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		backlog, err := source.Backlog(ctx)
		if err != nil {
			logger.Error("read reconciliation backlog", "error", err)
			http.Error(w, "backlog unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		for _, g := range []struct {
			name, help string
			value      float64
		}{
			{"ambagrid_api_reconciliation_open_cases", "Meter commands waiting on an operator decision.", float64(backlog.OpenCases)},
			{"ambagrid_api_reconciliation_oldest_open_case_age_seconds", "Age of the oldest open case since its command was requested; zero when none are open.", backlog.OldestOpenCase.Seconds()},
			{"ambagrid_api_sts_unvended_credits", "Confirmed STS credits with no vend and no recorded refund.", float64(backlog.UnvendedCredits)},
			{"ambagrid_api_sts_oldest_unvended_credit_age_seconds", "Age of the oldest unvended STS credit; zero when none remain.", backlog.OldestUnvendedSTS.Seconds()},
		} {
			fmt.Fprintf(w, "# HELP %[1]s %[2]s\n# TYPE %[1]s gauge\n%[1]s %[3]g\n", g.name, g.help, g.value)
		}
	})
	return mux
}
