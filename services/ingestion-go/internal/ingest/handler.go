// ingest receives telemetry batches from edge agents over HTTP, deduplicates
// on (site_id, sequence), validates each record, and forwards accepted records
// to Kafka as protobuf — the same wire format the MQTT bridge produces.
package ingest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type Handler struct {
	auth     Authenticator
	store    DedupStore
	producer Producer
	health   SiteHealthStore
	logger   *slog.Logger
	maxBody  int64
}

type HandlerConfig struct {
	Auth     Authenticator
	Store    DedupStore
	Producer Producer
	Health   SiteHealthStore
	Logger   *slog.Logger
	MaxBody  int64
}

func NewHandler(cfg HandlerConfig) *Handler {
	maxBody := cfg.MaxBody
	if maxBody <= 0 {
		maxBody = 5 << 20
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		auth:     cfg.Auth,
		store:    cfg.Store,
		producer: cfg.Producer,
		health:   cfg.Health,
		logger:   logger,
		maxBody:  maxBody,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", h.handleIngest)
	mux.HandleFunc("GET /healthz", h.handleHealth)
	mux.HandleFunc("/", h.handleNotFound)
	mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeErrorResponse(w, newRequestID(), http.StatusNotFound, "not_found", "no such endpoint")
}

func (h *Handler) handleIngest(w http.ResponseWriter, r *http.Request) {
	reqID := requestID(r)

	siteID, err := h.auth.Authenticate(r)
	if err != nil {
		h.logger.Warn("authentication failed", "error", err, "request_id", reqID)
		writeErrorResponse(w, reqID, http.StatusUnauthorized, "unauthenticated", "invalid or missing API key")
		return
	}

	body := http.MaxBytesReader(w, r.Body, h.maxBody)
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErrorResponse(w, reqID, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("request body exceeds %d bytes", h.maxBody))
			return
		}
		writeErrorResponse(w, reqID, http.StatusBadRequest, "invalid_argument", "failed to read request body")
		return
	}

	var batch BatchRequest
	if err := json.Unmarshal(data, &batch); err != nil {
		writeErrorResponse(w, reqID, http.StatusBadRequest, "invalid_argument", "malformed JSON")
		return
	}

	if err := validateHealth(batch); err != nil {
		writeErrorResponse(w, reqID, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}

	if err := h.health.RecordContact(r.Context(), SiteHealthReport{
		SiteID: siteID, GatewayID: batch.GatewayID, ContactAt: time.Now().UTC(),
		LastEventTimestamp: batch.LastEventTimestamp, QueueDepth: batch.QueueDepth,
		OldestPendingAt: batch.OldestPendingAt,
	}); err != nil {
		h.logger.Error("record site health", "error", err, "site_id", siteID, "request_id", reqID)
		writeErrorResponse(w, reqID, http.StatusInternalServerError, "internal", "failed to record site health")
		return
	}

	accepted := make([]uint64, 0, len(batch.Records))
	var rejected []RecordError

	for i, rec := range batch.Records {
		if fieldErr := validateRecord(rec, siteID); fieldErr != nil {
			rejected = append(rejected, RecordError{
				Index:    i,
				Sequence: rec.Sequence,
				Code:     "validation_failed",
				Message:  fieldErr.Error(),
			})
			continue
		}

		if rec.SiteID != siteID {
			rejected = append(rejected, RecordError{
				Index:    i,
				Sequence: rec.Sequence,
				Code:     "site_mismatch",
				Message:  fmt.Sprintf("record site_id %q does not match authenticated site %q", rec.SiteID, siteID),
			})
			continue
		}

		// TryInsert returns (true, nil) on first insert, (false, nil) if
		// already seen. Duplicates count as accepted so the edge agent
		// advances its cursor — the data is already in Kafka.
		stored, err := h.store.TryInsert(r.Context(), siteID, rec.Sequence)
		if err != nil {
			h.logger.Error("dedup store error", "error", err, "site_id", siteID, "sequence", rec.Sequence, "request_id", reqID)
			rejected = append(rejected, RecordError{
				Index:    i,
				Sequence: rec.Sequence,
				Code:     "internal",
				Message:  "internal error",
			})
			continue
		}
		if !stored {
			accepted = append(accepted, rec.Sequence)
			continue
		}

		if err := h.producer.Produce(r.Context(), siteID, rec); err != nil {
			h.logger.Error("kafka produce failed", "error", err, "site_id", siteID, "sequence", rec.Sequence, "request_id", reqID)
			// Roll back the dedup entry so the edge agent can retry this sequence.
			_ = h.store.Remove(r.Context(), siteID, rec.Sequence)
			rejected = append(rejected, RecordError{
				Index:    i,
				Sequence: rec.Sequence,
				Code:     "internal",
				Message:  "failed to enqueue record",
			})
			continue
		}

		accepted = append(accepted, rec.Sequence)
	}

	status := http.StatusOK
	if len(rejected) > 0 && len(accepted) == 0 {
		status = http.StatusUnprocessableEntity
	} else if len(rejected) > 0 {
		status = http.StatusMultiStatus
	}

	h.logger.Info("ingest batch",
		"site_id", siteID,
		"accepted", len(accepted),
		"rejected", len(rejected),
		"request_id", reqID,
	)

	writeJSON(w, status, BatchResponse{
		Accepted:  accepted,
		Rejected:  rejected,
		RequestID: reqID,
	})
}

func validateHealth(batch BatchRequest) error {
	if strings.TrimSpace(batch.GatewayID) == "" {
		return errors.New("gateway_id is required")
	}
	if batch.QueueDepth < 0 {
		return errors.New("queue_depth must not be negative")
	}
	if batch.QueueDepth == 0 && batch.OldestPendingAt != nil {
		return errors.New("oldest_pending_at must be null when queue_depth is zero")
	}
	if batch.QueueDepth > 0 && batch.OldestPendingAt == nil {
		return errors.New("oldest_pending_at is required when queue_depth is positive")
	}
	return nil
}

func validateRecord(rec IngestRecord, expectedSiteID string) error {
	var errs []string
	if rec.Sequence == 0 {
		errs = append(errs, "sequence must be greater than zero")
	}
	if strings.TrimSpace(rec.SiteID) == "" {
		errs = append(errs, "site_id is required")
	}
	if strings.TrimSpace(rec.GatewayID) == "" {
		errs = append(errs, "gateway_id is required")
	}
	if strings.TrimSpace(rec.DeviceID) == "" {
		errs = append(errs, "device_id is required")
	}
	if strings.TrimSpace(rec.MQTTTopic) == "" {
		errs = append(errs, "mqtt_topic is required")
	}
	if len(rec.Payload) == 0 {
		errs = append(errs, "payload must not be empty")
	}
	if rec.EventTimestamp.IsZero() {
		errs = append(errs, "event_timestamp is required")
	}
	if rec.UploadedAt.IsZero() {
		errs = append(errs, "uploaded_at is required")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func requestID(r *http.Request) string {
	id := r.Header.Get("X-Request-Id")
	if id != "" && len(id) <= 64 && isAlphanumeric(id) {
		return id
	}
	return newRequestID()
}

func isAlphanumeric(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErrorResponse(w http.ResponseWriter, requestID string, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{
		Code:      code,
		Message:   message,
		RequestID: requestID,
	}})
}

type ServerConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	Logger            *slog.Logger
}

func Serve(ctx context.Context, cfg ServerConfig, handler http.Handler) error {
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}

	serveErr := make(chan error, 1)
	go func() {
		cfg.Logger.Info("ingest http listening", "addr", listener.Addr().String())
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()

	cfg.Logger.Info("ingest http shutting down", "timeout", cfg.ShutdownTimeout.String())
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}
