// adapter defines the vendor-neutral contract every hardware integration implements.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"edge-agent-go/internal/domain"
)

var (
	ErrUnsupported = errors.New("adapter does not support this command or field")
	// ErrTimeout may expire before the device is contacted, e.g. while queued
	// behind another command, so it alone must not count as a device protocol timeout.
	ErrTimeout = errors.New("adapter operation exceeded its deadline")
	// ErrProtocolTimeout marks an adapter-configured device exchange timeout,
	// excluding caller deadlines and queued waits. It also matches ErrTimeout.
	ErrProtocolTimeout = fmt.Errorf("device protocol timed out: %w", ErrTimeout)
	ErrDeviceRejected  = errors.New("device rejected the command")
	ErrInvalidCommand  = errors.New("command is invalid")
	ErrInvalidResponse = errors.New("device response could not be decoded")
	// ErrOutcomeUnknown means the command reached the device but no reply
	// confirmed whether it was applied; resending could apply it twice.
	ErrOutcomeUnknown = errors.New("command outcome is unknown")
	// ErrSendHookSkipped means an adapter reported success without calling
	// BeforeSend, breaking the contract the ledger's at-most-once record relies on.
	ErrSendHookSkipped = errors.New("adapter applied a command without calling BeforeSend")
)

type TelemetryAdapter interface {
	Read(context.Context) ([]domain.Reading, error)
}

// CommandAdapter transports commands; Ledger provides at-most-once delivery.
// Call BeforeSend immediately before transmitting and use Command.ID as the vendor reference where supported.
type CommandAdapter interface {
	Execute(context.Context, Command) (CommandResult, error)
	Capabilities() Capabilities
}

// Reconciler is implemented by adapters that can ask the device whether a
// command reference was applied, which resolves an unknown outcome without resending.
type Reconciler interface {
	Outcome(context.Context, Command) (result CommandResult, applied bool, err error)
}

type CommandType string

const (
	CommandCredit        CommandType = "credit_meter"
	CommandDisconnect    CommandType = "disconnect_meter"
	CommandReconnect     CommandType = "reconnect_meter"
	CommandRequestStatus CommandType = "request_meter_status"
)

var CommandTypes = []CommandType{CommandCredit, CommandDisconnect, CommandReconnect, CommandRequestStatus}

type Command struct {
	ID        string      `json:"command_id"`
	Type      CommandType `json:"command_type"`
	DeviceID  string      `json:"device_id"`
	CreditKWh float64     `json:"credit_kwh,omitempty"`
	STSToken  string      `json:"sts_token,omitempty"`
	// RequestedAt orders relay commands for one device; zero when unknown.
	RequestedAt time.Time `json:"requested_at,omitzero"`
}

func (c Command) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("%w: command ID is required", ErrInvalidCommand)
	}
	if strings.TrimSpace(c.DeviceID) == "" {
		return fmt.Errorf("%w: device ID is required", ErrInvalidCommand)
	}
	if c.Type == CommandCredit {
		if c.CreditKWh <= 0 && c.STSToken == "" {
			return fmt.Errorf("%w: credit requires a positive kWh amount or an STS token", ErrInvalidCommand)
		}
		if c.CreditKWh > 0 && c.STSToken != "" {
			return fmt.Errorf("%w: credit takes either a kWh amount or an STS token, not both", ErrInvalidCommand)
		}
		return nil
	}
	if c.CreditKWh != 0 || c.STSToken != "" {
		return fmt.Errorf("%w: %s does not take credit fields", ErrInvalidCommand, c.Type)
	}
	return nil
}

type CommandResult struct {
	CommandID      string    `json:"command_id"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
	RelayClosed    *bool     `json:"relay_closed,omitempty"`
	BalanceKWh     *float64  `json:"balance_kwh,omitempty"`
	TamperDetected *bool     `json:"tamper_detected,omitempty"`
}

// Capabilities mirrors the device capability declaration in docs/hardware-adapters.md.
type Capabilities struct {
	DeviceType   domain.AssetType `yaml:"device_type"`
	Manufacturer string           `yaml:"manufacturer"`
	Model        string           `yaml:"model"`
	Capabilities CapabilitySet    `yaml:"capabilities"`
}

type CapabilitySet struct {
	Telemetry     bool `yaml:"telemetry"`
	RelayControl  bool `yaml:"relay_control"`
	PrepaidCredit bool `yaml:"prepaid_credit"`
	STSToken      bool `yaml:"sts_token"`
	// IdempotentRefs declares that the device ignores or rejects a repeated
	// command reference, so resending after an unknown outcome is safe.
	IdempotentRefs bool `yaml:"idempotent_refs"`
	// OutcomeQuery declares that the device can report whether it applied a
	// given command reference, which settles an unknown outcome without resending.
	OutcomeQuery bool `yaml:"outcome_query"`
}

func (c Capabilities) Supports(cmd Command) error {
	set := c.Capabilities
	var supported bool
	switch cmd.Type {
	case CommandCredit:
		supported = set.PrepaidCredit
		if supported && cmd.STSToken != "" && !set.STSToken {
			return fmt.Errorf("%w: %s %s does not accept STS tokens", ErrUnsupported, c.Manufacturer, c.Model)
		}
	case CommandDisconnect, CommandReconnect:
		supported = set.RelayControl
	case CommandRequestStatus:
		supported = set.Telemetry
	}
	if !supported {
		return fmt.Errorf("%w: %s %s does not support %q", ErrUnsupported, c.Manufacturer, c.Model, cmd.Type)
	}
	return nil
}

// ContextError keeps context.Canceled distinct from deadline expiry, which
// becomes ErrTimeout while still matching context.DeadlineExceeded.
func ContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrTimeout) {
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	}
	return err
}
