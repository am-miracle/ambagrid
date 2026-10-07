// Package health exposes local health and Prometheus metrics for the edge queue.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"edge-agent-go/internal/domain"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const statsTimeout = 2 * time.Second

func NewHandler(source StatsSource) http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(newCollector(source))

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/health", healthHandler(source))
	return mux
}

type healthResponse struct {
	State                domain.QueueState `json:"state"`
	QueueDepth           int64             `json:"queue_depth"`
	QueuePayloadBytes    int64             `json:"queue_payload_bytes"`
	QueueCapacityBytes   int64             `json:"queue_capacity_bytes"`
	QueueDiskBytes       int64             `json:"queue_disk_bytes"`
	FilesystemFreeBytes  int64             `json:"filesystem_free_bytes"`
	OldestPendingSeconds float64           `json:"oldest_pending_seconds"`
}

func healthHandler(source StatsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), statsTimeout)
		defer cancel()
		stats, err := source.Stats(ctx)
		if err != nil {
			http.Error(w, `{"state":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		statusCode := http.StatusOK
		if stats.State == domain.QueueFull || stats.State == domain.QueueFilesystemLow {
			statusCode = http.StatusInsufficientStorage
		}
		payload, err := json.Marshal(healthResponse{
			State:                stats.State,
			QueueDepth:           stats.Depth,
			QueuePayloadBytes:    stats.PayloadBytes,
			QueueCapacityBytes:   stats.CapacityBytes,
			QueueDiskBytes:       stats.DiskBytes,
			FilesystemFreeBytes:  stats.FilesystemFreeBytes,
			OldestPendingSeconds: stats.OldestAge.Seconds(),
		})
		if err != nil {
			http.Error(w, `{"state":"unavailable"}`, http.StatusInternalServerError)
			return
		}
		payload = append(payload, '\n')
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if _, err := w.Write(payload); err != nil {
			slog.Error("write edge health response", "error", err)
		}
	}
}
