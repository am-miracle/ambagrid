// uploader drains the durable queue in sequence order, POSTs batches to the
// ingestion service, and advances records through uploaded → acknowledged on
// success. Failures use exponential backoff via MarkFailed; the Ready query
// respects next_attempt_at so retries are gated by the store, not a timer.
package uploader

import (
	"context"
	"log/slog"
	"math"
	"time"

	"edge-agent-go/internal/domain"
)

type Config struct {
	GatewayID     string
	BatchSize     int
	BatchMaxBytes int64
	PollInterval  time.Duration
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	MaxRetries    int
}

type Uploader struct {
	cfg     Config
	queue   domain.Queue
	client  *IngestClient
	now     func() time.Time
	tracker UploadTracker
}

type UploadTracker interface {
	RecordFallbackUploadFailure(context.Context, uint64) error
	RecordFallbackUploadSuccess(context.Context, uint64, time.Time) error
	RecordUploadContactSuccess(context.Context, time.Time) error
	RecordUploadContactFailure(context.Context, string) error
}

func New(cfg Config, queue domain.Queue, client *IngestClient, tracker UploadTracker) *Uploader {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.BatchMaxBytes <= 0 {
		cfg.BatchMaxBytes = 1 << 20
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 1 * time.Second
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 5 * time.Minute
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 20
	}
	return &Uploader{cfg: cfg, queue: queue, client: client, tracker: tracker, now: time.Now}
}

func (u *Uploader) Run(ctx context.Context) {
	u.drainOnce(ctx)
	ticker := time.NewTicker(u.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.drainOnce(ctx)
		}
	}
}

// drainOnce sends consecutive batches until the queue is empty or an error
// occurs, so a single poll interval can clear a backlog without waiting.
func (u *Uploader) drainOnce(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := u.uploadBatch(ctx)
		if err != nil {
			slog.Error("upload batch", "error", err)
			return
		}
		if n == 0 {
			return
		}
	}
}

func (u *Uploader) uploadBatch(ctx context.Context) (int, error) {
	records, err := u.queue.Ready(ctx, u.cfg.BatchSize, u.cfg.BatchMaxBytes)
	if err != nil {
		return 0, err
	}
	stats, err := u.queue.Stats(ctx)
	if err != nil {
		return 0, err
	}
	// Stamp uploaded_at before the HTTP call so it reflects when the edge
	// agent sent the data, not when the server processed it.
	uploadedAt := u.now().UTC()

	// An empty batch is still sent as a gateway heartbeat.
	result, err := u.client.Upload(ctx, records, uploadedAt, HealthReport{
		GatewayID:            u.cfg.GatewayID,
		QueueDepth:           stats.Depth,
		OldestPendingAt:      stats.OldestPendingAt,
		LastEventTimestamp:   stats.LastEventTimestamp,
		LastSuccessfulSyncAt: stats.LastSuccessfulSyncAt,
		LastFailedSyncReason: stats.LastFailedSyncReason,
	})
	if err != nil {
		u.handleFailure(ctx, records, result, err)
		return 0, err
	}
	u.processResponse(ctx, records, result, uploadedAt)
	if u.tracker != nil {
		if err := u.tracker.RecordUploadContactSuccess(ctx, uploadedAt); err != nil {
			slog.Error("record upload contact", "error", err)
		}
	}
	return len(records), nil
}

func (u *Uploader) processResponse(ctx context.Context, records []domain.Record, result *UploadResult, uploadedAt time.Time) {
	if result.Response == nil {
		return
	}

	acceptedSet := make(map[uint64]bool, len(result.Response.Accepted))
	for _, seq := range result.Response.Accepted {
		acceptedSet[seq] = true
	}

	rejectedSet := make(map[uint64]RecordError, len(result.Response.Rejected))
	for _, rej := range result.Response.Rejected {
		rejectedSet[rej.Sequence] = rej
	}

	for _, r := range records {
		if acceptedSet[r.Sequence] {
			// The ingestion service only accepts a record after publishing it.
			// Keep the local row pending until that confirmation arrives so a
			// crash during the request cannot strand it in uploaded.
			if err := u.queue.MarkUploaded(ctx, r.Sequence, uploadedAt); err != nil {
				slog.Error("mark uploaded", "sequence", r.Sequence, "error", err)
				continue
			}
			if u.tracker != nil {
				if err := u.tracker.RecordFallbackUploadSuccess(ctx, r.Sequence, uploadedAt); err != nil {
					slog.Error("record critical upload success", "sequence", r.Sequence, "error", err)
					continue
				}
			}
			if err := u.queue.Ack(ctx, r.Sequence); err != nil {
				slog.Error("ack record", "sequence", r.Sequence, "error", err)
			}
			continue
		}

		if rej, ok := rejectedSet[r.Sequence]; ok {
			u.failRecord(ctx, r, rej.Code+": "+rej.Message)
			slog.Warn("record rejected by server",
				"sequence", r.Sequence,
				"code", rej.Code,
				"message", rej.Message,
			)
			continue
		}

		// A successful batch response must account for every record. Treat an
		// omitted sequence as retryable instead of spinning on it immediately.
		u.failRecord(ctx, r, "invalid ingestion response: sequence was neither accepted nor rejected")
	}
}

func (u *Uploader) handleFailure(ctx context.Context, records []domain.Record, result *UploadResult, uploadErr error) {
	cause := uploadErr.Error()
	if len(cause) > 2048 {
		cause = cause[:2048]
	}
	if u.tracker != nil {
		if err := u.tracker.RecordUploadContactFailure(ctx, cause); err != nil {
			slog.Error("record upload contact failure", "error", err)
		}
	}
	for _, r := range records {
		u.failRecord(ctx, r, cause)
	}
}

func (u *Uploader) failRecord(ctx context.Context, r domain.Record, cause string) {
	if r.AttemptCount+1 >= u.cfg.MaxRetries {
		slog.Error("record exhausted retries, dropping",
			"sequence", r.Sequence,
			"attempts", r.AttemptCount+1,
			"max_retries", u.cfg.MaxRetries,
			"last_error", cause,
		)
		if err := u.queue.Ack(ctx, r.Sequence); err != nil {
			slog.Error("ack exhausted record", "sequence", r.Sequence, "error", err)
		}
		u.recordFallbackFailure(ctx, r.Sequence)
		return
	}
	retryAt := u.retryTime(r.AttemptCount)
	if err := u.queue.MarkFailed(ctx, r.Sequence, retryAt, cause); err != nil {
		slog.Error("mark failed", "sequence", r.Sequence, "error", err)
	}
	u.recordFallbackFailure(ctx, r.Sequence)
}

func (u *Uploader) recordFallbackFailure(ctx context.Context, sequence uint64) {
	if u.tracker == nil {
		return
	}
	if err := u.tracker.RecordFallbackUploadFailure(ctx, sequence); err != nil {
		slog.Error("record critical upload failure", "sequence", sequence, "error", err)
	}
}

func (u *Uploader) retryTime(attemptCount int) time.Time {
	delay := float64(u.cfg.BaseDelay) * math.Pow(2, float64(attemptCount))
	if delay > float64(u.cfg.MaxDelay) {
		delay = float64(u.cfg.MaxDelay)
	}
	return u.now().UTC().Add(time.Duration(delay))
}
