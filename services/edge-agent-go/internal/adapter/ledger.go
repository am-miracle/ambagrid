package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type CommandState string

const (
	CommandInFlight CommandState = "in_flight"
	CommandApplied  CommandState = "applied"
	CommandRejected CommandState = "rejected"
	CommandUnknown  CommandState = "unknown"
)

type LedgerEntry struct {
	CommandID   string
	DeviceID    string
	Type        CommandType
	RequestHash []byte
	RequestedAt time.Time
	State       CommandState
	Attempts    int
	AcceptedAt  time.Time
	UpdatedAt   time.Time
	CompletedAt time.Time
	Result      *CommandResult
	LastError   string
}

// LedgerStore must commit each call durably before returning: the ledger
// relies on an in_flight row surviving power loss once BeginCommandAttempt succeeds.
type LedgerStore interface {
	LoadCommand(ctx context.Context, commandID string) (LedgerEntry, bool, error)
	// BeginCommandAttempt inserts entry as in_flight, or moves an unresolved
	// entry with the same request hash back to in_flight.
	BeginCommandAttempt(ctx context.Context, entry LedgerEntry, at time.Time) error
	CompleteCommand(ctx context.Context, commandID string, state CommandState, result *CommandResult, lastError string, at time.Time) error
	MarkCommandUnknown(ctx context.Context, commandID, lastError string, at time.Time) error
}

// Ledger gives any CommandAdapter at-most-once delivery per command ID across
// retries, lost replies, and restarts. An outcome it cannot establish is
// reported as ErrOutcomeUnknown rather than risking a second application.
type Ledger struct {
	store LedgerStore
	inner CommandAdapter
	now   func() time.Time

	mu     sync.Mutex
	owners map[string]chan struct{}
}

func NewLedger(store LedgerStore, inner CommandAdapter) *Ledger {
	return &Ledger{store: store, inner: inner, now: time.Now, owners: make(map[string]chan struct{})}
}

func (l *Ledger) Capabilities() Capabilities {
	return l.inner.Capabilities()
}

func (l *Ledger) Execute(ctx context.Context, cmd Command) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, ContextError(err)
	}
	caps := l.inner.Capabilities()
	if err := caps.Supports(cmd); err != nil {
		return CommandResult{}, err
	}
	if err := cmd.Validate(); err != nil {
		return CommandResult{}, err
	}
	hash, err := RequestHash(cmd)
	if err != nil {
		return CommandResult{}, err
	}
	release, err := l.own(ctx, cmd.ID)
	if err != nil {
		return CommandResult{}, err
	}
	defer release()

	entry, found, err := l.store.LoadCommand(ctx, cmd.ID)
	if err != nil {
		return CommandResult{}, fmt.Errorf("load command %q: %w", cmd.ID, ContextError(err))
	}
	if found {
		if !bytes.Equal(entry.RequestHash, hash) {
			return CommandResult{}, fmt.Errorf("%w: command ID %q was already used with different parameters", ErrInvalidCommand, cmd.ID)
		}
		switch entry.State {
		case CommandApplied:
			if entry.Result == nil {
				return CommandResult{}, fmt.Errorf("%w: applied command %q has no stored result", ErrOutcomeUnknown, cmd.ID)
			}
			return *entry.Result, nil
		case CommandRejected:
			reason := strings.TrimPrefix(entry.LastError, ErrDeviceRejected.Error()+": ")
			return CommandResult{}, fmt.Errorf("%w: %s", ErrDeviceRejected, reason)
		default:
			// in_flight here means an earlier attempt could not record its
			// outcome; no process owns it, so it is as unknown as after a restart.
			if result, settled, err := l.resolveUnknown(ctx, cmd, caps); settled {
				return result, err
			}
		}
	}
	return l.send(ctx, cmd, hash)
}

// own serializes callers of one command ID within this process; the durable
// in_flight row covers the gap across restarts.
func (l *Ledger) own(ctx context.Context, commandID string) (release func(), err error) {
	for {
		l.mu.Lock()
		busy, taken := l.owners[commandID]
		if !taken {
			done := make(chan struct{})
			l.owners[commandID] = done
			l.mu.Unlock()
			release = func() {
				l.mu.Lock()
				delete(l.owners, commandID)
				l.mu.Unlock()
				close(done)
			}
			// select picks randomly when the owner finished and ctx ended together.
			if err := ctx.Err(); err != nil {
				release()
				return nil, ContextError(err)
			}
			return release, nil
		}
		l.mu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, ContextError(ctx.Err())
		}
	}
}

// resolveUnknown reports settled when the caller must not send the command.
func (l *Ledger) resolveUnknown(ctx context.Context, cmd Command, caps Capabilities) (CommandResult, bool, error) {
	if reconciler, ok := l.inner.(Reconciler); ok {
		result, applied, err := reconciler.Outcome(ctx, cmd)
		if err != nil {
			return CommandResult{}, true, fmt.Errorf("%w: reconcile command %q: %w", ErrOutcomeUnknown, cmd.ID, ContextError(err))
		}
		if !applied {
			return CommandResult{}, false, nil
		}
		l.record(cmd.ID, l.store.CompleteCommand(context.WithoutCancel(ctx), cmd.ID, CommandApplied, &result, "", l.now().UTC()))
		return result, true, nil
	}
	if resendSafe(cmd, caps) {
		return CommandResult{}, false, nil
	}
	return CommandResult{}, true, fmt.Errorf("%w: command %q reached the device without confirmation; reconcile it before issuing a replacement", ErrOutcomeUnknown, cmd.ID)
}

func (l *Ledger) send(ctx context.Context, cmd Command, hash []byte) (CommandResult, error) {
	entry := LedgerEntry{CommandID: cmd.ID, DeviceID: cmd.DeviceID, Type: cmd.Type, RequestHash: hash, RequestedAt: cmd.RequestedAt}
	sent := false
	hooked := WithSendHook(ctx, func() error {
		if err := l.store.BeginCommandAttempt(ctx, entry, l.now().UTC()); err != nil {
			return fmt.Errorf("record command %q before sending: %w", cmd.ID, ContextError(err))
		}
		sent = true
		return nil
	})
	result, err := l.inner.Execute(hooked, cmd)

	// The device may have acted, so the outcome is recorded even if the caller gave up.
	persistCtx := context.WithoutCancel(ctx)
	now := l.now().UTC()
	if !sent {
		if err != nil {
			return result, err
		}
		// The adapter broke its contract, but the device acted: record it as
		// applied so a retry finds it instead of sending it again.
		if recordErr := l.store.BeginCommandAttempt(persistCtx, entry, now); recordErr != nil {
			l.record(cmd.ID, recordErr)
		} else {
			l.record(cmd.ID, l.store.CompleteCommand(persistCtx, cmd.ID, CommandApplied, &result, "", now))
		}
		return result, fmt.Errorf("%w: command %q", ErrSendHookSkipped, cmd.ID)
	}

	switch {
	case err == nil:
		l.record(cmd.ID, l.store.CompleteCommand(persistCtx, cmd.ID, CommandApplied, &result, "", now))
		return result, nil
	case errors.Is(err, ErrDeviceRejected):
		l.record(cmd.ID, l.store.CompleteCommand(persistCtx, cmd.ID, CommandRejected, nil, err.Error(), now))
		return CommandResult{}, err
	default:
		l.record(cmd.ID, l.store.MarkCommandUnknown(persistCtx, cmd.ID, err.Error(), now))
		return CommandResult{}, fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
}

// record logs rather than returns a failure to store an outcome the device has
// already produced. The row stays in_flight, which later reads treat as unknown.
func (l *Ledger) record(commandID string, err error) {
	if err != nil {
		slog.Error("record command outcome", "command_id", commandID, "error", err)
	}
}

func resendSafe(cmd Command, caps Capabilities) bool {
	switch {
	case cmd.Type != CommandCredit:
		return true // relay and status commands set or read absolute state
	case cmd.STSToken != "":
		return true // STS meters refuse a token they have already accepted
	default:
		return caps.Capabilities.IdempotentRefs
	}
}

// RequestHash identifies a command's parameters, so a reused ID with
// different parameters can be detected.
func RequestHash(cmd Command) ([]byte, error) {
	canonical, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("encode command %q: %w", cmd.ID, err)
	}
	sum := sha256.Sum256(canonical)
	return sum[:], nil
}
