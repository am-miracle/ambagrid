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
	actorHeaders := r.Header.Values(actorIDHeader)
	if len(actorHeaders) == 0 {
		writeErrorBody(w, a.logger(), http.StatusUnauthorized, codeUnauthenticated, "missing trusted actor identity", RequestIDFrom(r.Context()))
		return
	}

	resolvedBy := r.Header.Get(actorIDHeader)

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

type applyDevPaymentBody struct {
	CustomerID       string `json:"customer_id"`
	AmountMinorUnits int64  `json:"amount_minor_units"`
	Currency         string `json:"currency"`
}

const maxWebhookBodySize = 64 * 1024

func (a API) handleWebhookPayment(provider WebhookProvider) http.HandlerFunc {
	signatureHeader := provider.SignatureHeader()

	return func(w http.ResponseWriter, r *http.Request) {
		signature := r.Header.Get(signatureHeader)
		if signature == "" {
			writeErrorBody(w, a.logger(), http.StatusUnauthorized, codeUnauthenticated, "missing webhook signature", RequestIDFrom(r.Context()))
			return
		}

		// Providers sign the exact request bytes, so verify the untouched body
		// before parsing or normalizing any fields.
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodySize))
		if err != nil {
			writeError(w, r, a.logger(), errInvalidParameter)
			return
		}

		if err := provider.VerifySignature(body, signature); err != nil {
			a.logger().Warn("webhook signature verification failed",
				"provider", provider.Name(),
				"error", err,
				"request_id", RequestIDFrom(r.Context()),
			)
			writeErrorBody(w, a.logger(), http.StatusUnauthorized, codeUnauthenticated, "invalid webhook signature", RequestIDFrom(r.Context()))
			return
		}

		command, err := provider.ParsePayment(body)
		if err != nil {
			a.logger().Warn("webhook payload rejected",
				"provider", provider.Name(),
				"error", err,
				"request_id", RequestIDFrom(r.Context()),
			)
			writeErrorBody(w, a.logger(), http.StatusBadRequest, codeInvalidArgument, err.Error(), RequestIDFrom(r.Context()))
			return
		}

		result, err := a.Payments.ApplyWebhookPayment(r.Context(), command)
		if err != nil {
			writeError(w, r, a.logger(), err)
			return
		}

		writeJSON(w, a.logger(), http.StatusOK, objectBody[applyPaymentResultDTO]{
			Data:      toApplyPaymentResultDTO(result),
			RequestID: RequestIDFrom(r.Context()),
		})
	}
}

func (a API) handleApplyDevPayment(w http.ResponseWriter, r *http.Request) {
	var body applyDevPaymentBody
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

	result, err := a.Payments.ApplyDevPayment(r.Context(), services.ApplyDevPaymentRequest{
		CustomerID:       body.CustomerID,
		AmountMinorUnits: body.AmountMinorUnits,
		Currency:         body.Currency,
	})
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}

	writeJSON(w, a.logger(), http.StatusCreated, objectBody[applyPaymentResultDTO]{
		Data:      toApplyPaymentResultDTO(result),
		RequestID: RequestIDFrom(r.Context()),
	})
}

func (a API) handleListDevCustomers(w http.ResponseWriter, r *http.Request) {
	customers, err := a.Payments.ListCustomers(r.Context())
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	items := make([]customerSummaryDTO, 0, len(customers))
	for _, cs := range customers {
		items = append(items, toCustomerSummaryDTO(cs))
	}
	writeJSON(w, a.logger(), http.StatusOK, collectionBody[customerSummaryDTO]{
		Data:      items,
		Page:      pageMetadata{Limit: len(items)},
		RequestID: RequestIDFrom(r.Context()),
	})
}

func (a API) handleListDevCommands(w http.ResponseWriter, r *http.Request) {
	commands, err := a.Payments.ListMeterCommands(r.Context())
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	items := make([]meterCommandDTO, 0, len(commands))
	for _, cmd := range commands {
		items = append(items, toMeterCommandDTO(cmd))
	}
	writeJSON(w, a.logger(), http.StatusOK, collectionBody[meterCommandDTO]{
		Data:      items,
		Page:      pageMetadata{Limit: len(items)},
		RequestID: RequestIDFrom(r.Context()),
	})
}

func (a API) handleListDevAuditEvents(w http.ResponseWriter, r *http.Request) {
	var customerID string
	if v := optionalParam(r, "customer_id"); v != nil {
		customerID = *v
	}
	events, err := a.Payments.ListAuditEvents(r.Context(), customerID)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	items := make([]auditEventDTO, 0, len(events))
	for _, ev := range events {
		items = append(items, toAuditEventDTO(ev))
	}
	writeJSON(w, a.logger(), http.StatusOK, collectionBody[auditEventDTO]{
		Data:      items,
		Page:      pageMetadata{Limit: len(items)},
		RequestID: RequestIDFrom(r.Context()),
	})
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
