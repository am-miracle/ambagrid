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
	a.handleRoute(mux, http.MethodGet, "/healthz", a.handleLive)
	a.handleRoute(mux, http.MethodGet, "/readyz", a.handleReady)

	a.handleRoute(mux, http.MethodGet, "/v1/sites", a.handleListSites)
	a.handleRoute(mux, http.MethodGet, "/v1/assets", a.handleListAssets)
	a.handleRoute(mux, http.MethodGet, "/v1/assets/{asset_id}", a.handleGetAsset)
	a.handleRoute(mux, http.MethodGet, "/v1/assets/{asset_id}/readings", a.handleGetAssetReadings)
	a.handleRoute(mux, http.MethodGet, "/v1/alerts", a.handleListAlerts)
	a.handleRoute(mux, http.MethodGet, "/v1/alerts/{alert_id}", a.handleGetAlert)
	a.handleRoute(mux, http.MethodPost, "/v1/alerts/{alert_id}/resolve", a.handleResolveAlert)

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

func (a API) handleRoute(mux *http.ServeMux, method, pattern string, handler http.HandlerFunc) {
	mux.HandleFunc(method+" "+pattern, handler)
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", method+", OPTIONS")
		writeErrorBody(w, a.logger(), http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed", RequestIDFrom(r.Context()))
	})
}

func (a API) handleUnmatched(w http.ResponseWriter, r *http.Request) {
	writeErrorBody(w, a.logger(), http.StatusNotFound, codeNotFound, "no such endpoint", RequestIDFrom(r.Context()))
}
