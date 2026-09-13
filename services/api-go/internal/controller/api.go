// Defines API routes and middleware order.
package controller

import (
	"log/slog"
	"net/http"
	"time"
)

type API struct {
	Assets         AssetService
	Alerts         AlertService
	Sites          SiteService
	Health         HealthService
	Logger         *slog.Logger
	RequestTimeout time.Duration
	AllowedOrigins []string
}

// Handler builds the versioned routes and middleware chain.
func (a API) Handler() http.Handler {
	mux := http.NewServeMux()

	// Liveness does not depend on the database; readiness does.
	mux.HandleFunc("GET /healthz", a.handleLive)
	mux.HandleFunc("GET /readyz", a.handleReady)

	mux.HandleFunc("GET /v1/sites", a.handleListSites)
	mux.HandleFunc("GET /v1/assets", a.handleListAssets)
	mux.HandleFunc("GET /v1/assets/{asset_id}", a.handleGetAsset)
	mux.HandleFunc("GET /v1/alerts", a.handleListAlerts)
	mux.HandleFunc("GET /v1/alerts/{alert_id}", a.handleGetAlert)

	// Keep unmatched responses in the API's JSON envelope.
	mux.HandleFunc("/", a.handleUnmatched)

	// Assign the request ID before recovery so panic reports retain it.
	return withRequestID(
		recoverPanics(a.logger())(
			logRequests(a.logger())(
				withTimeout(a.RequestTimeout)(
					withCORS(a.AllowedOrigins)(mux),
				),
			),
		),
	)
}

func (a API) logger() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

func (a API) handleUnmatched(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		writeErrorBody(w, a.logger(), http.StatusMethodNotAllowed, codeMethodNotAllowed, "this API is read-only", RequestIDFrom(r.Context()))
		return
	}

	writeErrorBody(w, a.logger(), http.StatusNotFound, codeNotFound, "no such endpoint", RequestIDFrom(r.Context()))
}
