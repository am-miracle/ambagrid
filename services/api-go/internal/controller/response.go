// defines JSON envelopes and error-to-status mapping.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"api-go/internal/domain"
	"api-go/internal/page"
	"api-go/internal/services"
)

// envelopes keep response metadata consistent across endpoints.
type collectionBody[T any] struct {
	Data      []T          `json:"data"`
	Page      pageMetadata `json:"page"`
	RequestID string       `json:"request_id"`
}

type pageMetadata struct {
	Limit      int    `json:"limit"`
	NextCursor string `json:"next_cursor"`
}

type objectBody[T any] struct {
	Data      T      `json:"data"`
	RequestID string `json:"request_id"`
}

type statusBody struct {
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	RequestID string `json:"request_id"`
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

const (
	codeInvalidArgument  = "invalid_argument"
	codeNotFound         = "not_found"
	codeMethodNotAllowed = "method_not_allowed"
	codeDeadlineExceeded = "deadline_exceeded"
	codeInternal         = "internal"
)

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status is already written, so only logging remains possible.
		logger.Error("write response body", "error", err)
	}
}

// writeCollection emits an empty JSON array rather than null when exhausted.
func writeCollection[T any, W any](w http.ResponseWriter, r *http.Request, logger *slog.Logger, result page.Page[T], convert func(T) W) {
	items := make([]W, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, convert(item))
	}
	writeJSON(w, logger, http.StatusOK, collectionBody[W]{
		Data:      items,
		Page:      pageMetadata{Limit: result.Limit, NextCursor: result.NextCursor},
		RequestID: RequestIDFrom(r.Context()),
	})
}

func writeObject[T any](w http.ResponseWriter, r *http.Request, logger *slog.Logger, body T) {
	writeJSON(w, logger, http.StatusOK, objectBody[T]{Data: body, RequestID: RequestIDFrom(r.Context())})
}

func writeStatus(w http.ResponseWriter, r *http.Request, logger *slog.Logger, statusCode int, status, reason string) {
	writeJSON(w, logger, statusCode, statusBody{
		Status:    status,
		Reason:    reason,
		RequestID: RequestIDFrom(r.Context()),
	})
}

// writeError maps known failures and hides unknown causes.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	requestID := RequestIDFrom(r.Context())

	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeErrorBody(w, logger, http.StatusNotFound, codeNotFound, "object not found", requestID)
	case errors.Is(err, page.ErrInvalidCursor):
		writeErrorBody(w, logger, http.StatusBadRequest, codeInvalidArgument, "cursor is not a cursor this API issued", requestID)
	case errors.Is(err, domain.ErrInvalidID),
		errors.Is(err, services.ErrInvalidRequest),
		errors.Is(err, errInvalidParameter):
		writeErrorBody(w, logger, http.StatusBadRequest, codeInvalidArgument, err.Error(), requestID)
	case errors.Is(err, context.DeadlineExceeded):
		logger.Warn("request deadline exceeded", "error", err, "request_id", requestID, "path", r.URL.Path)
		writeErrorBody(w, logger, http.StatusGatewayTimeout, codeDeadlineExceeded, "the request took too long", requestID)
	case errors.Is(err, context.Canceled):
		// The client disconnected, so log without writing a response.
		logger.Debug("request canceled by client", "request_id", requestID, "path", r.URL.Path)
	case errors.Is(err, domain.ErrInvalidData):
		logger.Error("corrupt data read from store", "error", err, "request_id", requestID, "path", r.URL.Path)
		writeErrorBody(w, logger, http.StatusInternalServerError, codeInternal, "internal error", requestID)
	default:
		logger.Error("request failed", "error", err, "request_id", requestID, "path", r.URL.Path)
		writeErrorBody(w, logger, http.StatusInternalServerError, codeInternal, "internal error", requestID)
	}
}

func writeErrorBody(w http.ResponseWriter, logger *slog.Logger, status int, code, message, requestID string) {
	writeJSON(w, logger, status, errorBody{Error: errorDetail{
		Code:      code,
		Message:   message,
		RequestID: requestID,
	}})
}
