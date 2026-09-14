// handles HTTP endpoints and delegates work to services.
package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"api-go/internal/domain"
	"api-go/internal/services"
)

func parseListAssetsRequest(r *http.Request) (services.ListAssetsRequest, error) {
	limit, err := limitParam(r)
	if err != nil {
		return services.ListAssetsRequest{}, err
	}
	assetType, err := enumParam(r, "asset_type", domain.ParseAssetType)
	if err != nil {
		return services.ListAssetsRequest{}, err
	}

	return services.ListAssetsRequest{
		Filter: domain.AssetFilter{
			SiteID:    optionalParam(r, "site_id"),
			AssetType: assetType,
		},
		Limit:  limit,
		Cursor: cursorParam(r),
	}, nil
}

func (a API) handleListAssets(w http.ResponseWriter, r *http.Request) {
	request, err := parseListAssetsRequest(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	result, err := a.Assets.List(r.Context(), request)
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

func parseGetAssetReadingsRequest(r *http.Request) (services.GetReadingSeriesRequest, error) {
	from, err := requiredTimeParam(r, "from")
	if err != nil {
		return services.GetReadingSeriesRequest{}, err
	}
	to, err := requiredTimeParam(r, "to")
	if err != nil {
		return services.GetReadingSeriesRequest{}, err
	}
	metric, err := requiredEnumParam(r, "metric", domain.ParseReadingMetric)
	if err != nil {
		return services.GetReadingSeriesRequest{}, err
	}
	interval, err := requiredEnumParam(r, "interval", domain.ParseReadingInterval)
	if err != nil {
		return services.GetReadingSeriesRequest{}, err
	}

	return services.GetReadingSeriesRequest{
		From:     from,
		To:       to,
		Metric:   metric,
		Interval: interval,
	}, nil
}

func (a API) handleGetAssetReadings(w http.ResponseWriter, r *http.Request) {
	request, err := parseGetAssetReadingsRequest(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	series, err := a.Assets.Readings(r.Context(), r.PathValue("asset_id"), request)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeObject(w, r, a.logger(), toReadingSeriesDTO(series))
}

func parseListAlertsRequest(r *http.Request) (services.ListAlertsRequest, error) {
	limit, err := limitParam(r)
	if err != nil {
		return services.ListAlertsRequest{}, err
	}
	status, err := enumParam(r, "status", domain.ParseAlertStatus)
	if err != nil {
		return services.ListAlertsRequest{}, err
	}
	severity, err := enumParam(r, "severity", domain.ParseSeverity)
	if err != nil {
		return services.ListAlertsRequest{}, err
	}

	return services.ListAlertsRequest{
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
	}, nil
}

func (a API) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	request, err := parseListAlertsRequest(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	result, err := a.Alerts.List(r.Context(), request)
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

type resolveAlertBody struct {
	ResolutionNote string `json:"resolution_note"`
}

func (a API) handleResolveAlert(w http.ResponseWriter, r *http.Request) {
	operatorHeaders := r.Header.Values(operatorIDHeader)
	if len(operatorHeaders) == 0 {
		writeErrorBody(w, a.logger(), http.StatusUnauthorized, codeUnauthenticated, "missing trusted operator identity", RequestIDFrom(r.Context()))
		return
	}

	resolvedBy := r.Header.Get(operatorIDHeader)

	var body resolveAlertBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, r, a.logger(), errInvalidParameter)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, a.logger(), errInvalidParameter)
		return
	}

	alert, err := a.Alerts.Resolve(r.Context(), r.PathValue("alert_id"), services.ResolveAlertRequest{
		ResolutionNote: body.ResolutionNote,
		ResolvedBy:     resolvedBy,
	})
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeObject(w, r, a.logger(), toAlertDTO(alert))
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
