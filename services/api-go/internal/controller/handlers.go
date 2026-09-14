// handles HTTP endpoints and delegates work to services.
package controller

import (
	"net/http"

	"api-go/internal/domain"
	"api-go/internal/services"
)

func (a API) handleListAssets(w http.ResponseWriter, r *http.Request) {
	limit, err := limitParam(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	assetType, err := enumParam(r, "asset_type", domain.ParseAssetType)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	result, err := a.Assets.List(r.Context(), services.ListAssetsRequest{
		Filter: domain.AssetFilter{
			SiteID:    optionalParam(r, "site_id"),
			AssetType: assetType,
		},
		Limit:  limit,
		Cursor: cursorParam(r),
	})
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeCollection(w, r, a.logger(), result, toAssetDTO)
}

func (a API) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	asset, err := a.Assets.Get(r.Context(), r.PathValue("asset_id"))
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeObject(w, r, a.logger(), toAssetDTO(asset))
}

func (a API) handleGetAssetReadings(w http.ResponseWriter, r *http.Request) {
	from, err := requiredTimeParam(r, "from")
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	to, err := requiredTimeParam(r, "to")
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	metric, err := requiredEnumParam(r, "metric", domain.ParseReadingMetric)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	interval, err := requiredEnumParam(r, "interval", domain.ParseReadingInterval)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	series, err := a.Assets.Readings(r.Context(), r.PathValue("asset_id"), services.GetReadingSeriesRequest{
		From:     from,
		To:       to,
		Metric:   metric,
		Interval: interval,
	})
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeObject(w, r, a.logger(), toReadingSeriesDTO(series))
}

func (a API) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	limit, err := limitParam(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	status, err := enumParam(r, "status", domain.ParseAlertStatus)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	severity, err := enumParam(r, "severity", domain.ParseSeverity)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	result, err := a.Alerts.List(r.Context(), services.ListAlertsRequest{
		Filter: domain.AlertFilter{
			SiteID:   optionalParam(r, "site_id"),
			AssetID:  optionalParam(r, "asset_id"),
			Status:   status,
			Severity: severity,
			// alert kinds are open-ended, unlike status and severity.
			Kind: optionalParam(r, "kind"),
		},
		Limit:  limit,
		Cursor: cursorParam(r),
	})
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeCollection(w, r, a.logger(), result, toAlertDTO)
}

func (a API) handleGetAlert(w http.ResponseWriter, r *http.Request) {
	detail, err := a.Alerts.Get(r.Context(), r.PathValue("alert_id"))
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeObject(w, r, a.logger(), toAlertDetailDTO(detail))
}

func (a API) handleListSites(w http.ResponseWriter, r *http.Request) {
	limit, err := limitParam(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	result, err := a.Sites.List(r.Context(), services.ListSitesRequest{
		Limit:  limit,
		Cursor: cursorParam(r),
	})
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeCollection(w, r, a.logger(), result, toSiteDTO)
}

func (a API) handleLive(w http.ResponseWriter, r *http.Request) {
	writeStatus(w, r, a.logger(), http.StatusOK, "ok", "")
}

func (a API) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := a.Health.Ready(r.Context()); err != nil {
		a.logger().Warn("readiness check failed", "error", err, "request_id", RequestIDFrom(r.Context()))
		writeStatus(w, r, a.logger(), http.StatusServiceUnavailable, "unavailable", "database unreachable")
		return
	}

	writeStatus(w, r, a.logger(), http.StatusOK, "ok", "")
}
