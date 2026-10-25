// commands delivers platform meter commands to local hardware through the
// command ledger and reports each outcome back to the platform.
package commands

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

const (
	reportBatch     = 50
	reportBaseDelay = 5 * time.Second
	reportMaxDelay  = 10 * time.Minute
)

type Platform interface {
	Poll(context.Context) ([]Pending, error)
	Report(context.Context, domain.CommandReport) error
}

type Store interface {
	LoadCommand(ctx context.Context, commandID string) (adapter.LedgerEntry, bool, error)
	RefuseCommand(ctx context.Context, entry adapter.LedgerEntry, status domain.CommandReportStatus, reason string, at time.Time) (bool, error)
	LastRelayCommand(ctx context.Context, deviceID string) (time.Time, string, bool, error)
	DueCommandReports(ctx context.Context, now time.Time, limit int) ([]domain.CommandReport, error)
	DeliveredCommandReport(ctx context.Context, id int64) error
	DeferCommandReport(ctx context.Context, id int64, next time.Time, lastError string) error
	AbandonCommandReport(ctx context.Context, id int64, at time.Time, lastError string) error
}

type Config struct {
	PollInterval   time.Duration
	CommandTimeout time.Duration
}

// Runner handles one command at a time in request order, which is what lets
// the relay ordering rule compare a command against everything sent before it.
type Runner struct {
	cfg      Config
	platform Platform
	executor adapter.CommandAdapter
	store    Store
	now      func() time.Time
}

// New expects executor to be an *adapter.Ledger, which supplies at-most-once delivery.
func New(cfg Config, platform Platform, executor adapter.CommandAdapter, store Store) *Runner {
	return &Runner{cfg: cfg, platform: platform, executor: executor, store: store, now: time.Now}
}

// SetClock makes expiry and report retry times deterministic in tests.
func (r *Runner) SetClock(now func() time.Time) {
	r.now = now
}

func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.SyncOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("sync meter commands", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SyncOnce sends queued reports, runs pending commands, then sends the
// reports those commands produced.
func (r *Runner) SyncOnce(ctx context.Context) error {
	deliverErr := r.deliverReports(ctx)
	pending, pollErr := r.platform.Poll(ctx)
	var handleErr error
	if pollErr == nil {
		slices.SortStableFunc(pending, func(a, b Pending) int {
			if c := a.RequestedAt.Compare(b.RequestedAt); c != 0 {
				return c
			}
			return cmp.Compare(a.CommandID, b.CommandID)
		})
		for _, p := range pending {
			if ctx.Err() != nil {
				break
			}
			handleErr = errors.Join(handleErr, r.handle(ctx, p))
		}
	}
	if deliverErr == nil {
		deliverErr = r.deliverReports(ctx)
	}
	return errors.Join(deliverErr, pollErr, handleErr)
}

func (r *Runner) handle(ctx context.Context, p Pending) error {
	cmd := adapter.Command{
		ID: p.CommandID, Type: adapter.CommandType(p.CommandType),
		DeviceID: p.DeviceID, CreditKWh: p.CreditKWh, RequestedAt: p.RequestedAt.UTC(),
	}
	entry, found, err := r.store.LoadCommand(ctx, cmd.ID)
	if err != nil {
		return err
	}
	if found && (entry.State == adapter.CommandApplied || entry.State == adapter.CommandRejected) {
		return nil // its report was queued with the transition
	}

	// r.now keeps Go's monotonic reading, which Deadline is measured on; .UTC()
	// would strip it and bring the gateway wall clock back into the comparison.
	if !r.now().Before(p.deadline()) {
		// An expired command that may already have reached the device stays
		// unresolved for reconciliation; it is never resent after its deadline.
		if !found {
			return r.refuse(ctx, cmd, domain.ReportExpired, fmt.Sprintf("expired at %s before it was sent", p.ExpiresAt.UTC().Format(time.RFC3339)))
		}
		return nil
	}
	if superseded, newer, err := r.superseded(ctx, cmd); err != nil {
		return err
	} else if superseded {
		if !found {
			return r.refuse(ctx, cmd, domain.ReportFailed, fmt.Sprintf("superseded by newer relay command %s", newer))
		}
		return nil
	}

	execCtx, cancel := context.WithTimeout(ctx, min(r.cfg.CommandTimeout, p.deadline().Sub(r.now())))
	defer cancel()
	_, err = r.executor.Execute(execCtx, cmd)
	switch {
	case err == nil, errors.Is(err, adapter.ErrDeviceRejected):
		return nil
	case errors.Is(err, adapter.ErrSendHookSkipped):
		// Applied and recorded by the ledger; the adapter is broken, not the command.
		slog.Error("command adapter skipped BeforeSend", "command_id", cmd.ID, "error", err)
		return nil
	case errors.Is(err, adapter.ErrOutcomeUnknown):
		if found && entry.State == adapter.CommandUnknown {
			slog.Debug("meter command still unresolved", "command_id", cmd.ID, "error", err)
		} else {
			slog.Warn("meter command outcome unknown", "command_id", cmd.ID, "device_id", cmd.DeviceID, "error", err)
		}
		return nil
	case !found && (errors.Is(err, adapter.ErrUnsupported) || errors.Is(err, adapter.ErrInvalidCommand)):
		return r.refuse(ctx, cmd, domain.ReportFailed, err.Error())
	default:
		// Not sent, for example timed out waiting for the meter link; the
		// next poll returns it again.
		return fmt.Errorf("run command %q: %w", cmd.ID, err)
	}
}

// superseded reports whether a newer relay command for the same meter was
// already sent; applying an older one now would leave the wrong relay state.
func (r *Runner) superseded(ctx context.Context, cmd adapter.Command) (bool, string, error) {
	if cmd.Type != adapter.CommandDisconnect && cmd.Type != adapter.CommandReconnect {
		return false, "", nil
	}
	lastAt, lastID, found, err := r.store.LastRelayCommand(ctx, cmd.DeviceID)
	if err != nil || !found || lastID == cmd.ID {
		return false, "", err
	}
	newer := lastAt.After(cmd.RequestedAt) || lastAt.Equal(cmd.RequestedAt) && lastID > cmd.ID
	return newer, lastID, nil
}

func (p Pending) deadline() time.Time {
	if p.Deadline.IsZero() {
		return p.ExpiresAt
	}
	return p.Deadline
}

func (r *Runner) refuse(ctx context.Context, cmd adapter.Command, status domain.CommandReportStatus, reason string) error {
	hash, err := adapter.RequestHash(cmd)
	if err != nil {
		return err
	}
	entry := adapter.LedgerEntry{CommandID: cmd.ID, DeviceID: cmd.DeviceID, Type: cmd.Type, RequestHash: hash, RequestedAt: cmd.RequestedAt}
	if _, err := r.store.RefuseCommand(ctx, entry, status, reason, r.now().UTC()); err != nil {
		return err
	}
	slog.Info("refused meter command", "command_id", cmd.ID, "status", status, "reason", reason)
	return nil
}

// deliverReports runs until nothing is due, since delivering a report can make
// the same command's next report due. It stops at the first transient failure
// because the link is probably down, and refuses to handle a report twice so a
// store that keeps returning it cannot spin the loop.
func (r *Runner) deliverReports(ctx context.Context) error {
	handled := make(map[int64]bool)
	for {
		now := r.now().UTC()
		due, err := r.store.DueCommandReports(ctx, now, reportBatch)
		if err != nil || len(due) == 0 {
			return err
		}
		for _, report := range due {
			if handled[report.ID] {
				return fmt.Errorf("command report %d is still due after it was handled", report.ID)
			}
			handled[report.ID] = true
			err := r.platform.Report(ctx, report)
			switch {
			case err == nil:
				err = r.store.DeliveredCommandReport(ctx, report.ID)
			case errors.Is(err, ErrReportRefused):
				slog.Error("platform refused meter command report", "command_id", report.CommandID, "status", report.Status, "error", err)
				err = r.store.AbandonCommandReport(ctx, report.ID, now, err.Error())
			default:
				if deferErr := r.store.DeferCommandReport(ctx, report.ID, now.Add(reportBackoff(report.Attempts)), err.Error()); deferErr != nil {
					return errors.Join(err, deferErr)
				}
				return err
			}
			if err != nil {
				return err
			}
		}
	}
}

func reportBackoff(attempts int) time.Duration {
	delay := reportBaseDelay
	for range attempts {
		delay *= 2
		if delay >= reportMaxDelay {
			return reportMaxDelay
		}
	}
	return delay
}
