// adapter defines the vendor-neutral contract every hardware integration implements.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"edge-agent-go/internal/domain"
)

var (
	ErrFirmwareMismatch = fmt.Errorf("firmware is missing or incompatible: %w", ErrUnsupported)
	ErrUnsupported      = errors.New("adapter does not support this command or field")
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
// Call send.BeforeSend immediately before transmitting and use Command.ID as the vendor reference where supported.
type CommandAdapter interface {
	Execute(context.Context, Command, SendGate) (CommandResult, error)
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
		if c.CreditKWh <= 0 || math.IsNaN(c.CreditKWh) || math.IsInf(c.CreditKWh, 0) || c.CreditKWh >= float64(math.MaxInt64)/1000 {
			return fmt.Errorf("%w: credit requires a finite positive kWh amount within the meter range", ErrInvalidCommand)
		}
		return nil
	}
	if c.CreditKWh != 0 {
		return fmt.Errorf("%w: %s does not take credit fields", ErrInvalidCommand, c.Type)
	}

	return nil
}

type CommandResult struct {
	FirmwareVersion string    `json:"firmware_version,omitempty"`
	FaultCode       string    `json:"fault_code,omitempty"`
	CommandID       string    `json:"command_id"`
	AcknowledgedAt  time.Time `json:"acknowledged_at"`
	RelayClosed     *bool     `json:"relay_closed,omitempty"`
	BalanceKWh      *float64  `json:"balance_kwh,omitempty"`
	TamperDetected  *bool     `json:"tamper_detected,omitempty"`
}

func (r CommandResult) Observation() domain.CommandObservation {
	return domain.CommandObservation{RelayClosed: r.RelayClosed, BalanceKWh: r.BalanceKWh, TamperDetected: r.TamperDetected, FirmwareVersion: r.FirmwareVersion, FaultCode: r.FaultCode}
}

type FaultCode struct {
	Description string `yaml:"description"`
	Severity    string `yaml:"severity"`
}

// Capabilities mirrors the device capability declaration in docs/hardware-adapters.md.
type Capabilities struct {
	FirmwareVersions []string             `yaml:"firmware_versions"`
	FaultCodes       map[string]FaultCode `yaml:"fault_codes"`
	// CommandTemplates replaces the default request body per command type.
	CommandTemplates map[CommandType]string `yaml:"command_templates"`
	DeviceType       domain.AssetType       `yaml:"device_type"`
	Manufacturer     string                 `yaml:"manufacturer"`
	Model            string                 `yaml:"model"`
	Capabilities     CapabilitySet          `yaml:"capabilities"`
}

// FirmwareAllowed is false only when a policy exists and version is not on it.
func (c Capabilities) FirmwareAllowed(version string) bool {
	return len(c.FirmwareVersions) == 0 || slices.Contains(c.FirmwareVersions, version)
}

type CapabilitySet struct {
	Telemetry     bool `yaml:"telemetry"`
	RelayControl  bool `yaml:"relay_control"`
	PrepaidCredit bool `yaml:"prepaid_credit"`
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

// ProtocolDeadlineExceeded checks for exchange expiry while the caller is active.
// Callers must confirm protocol work began before returning ErrProtocolTimeout.
func ProtocolDeadlineExceeded(caller, exchange context.Context) bool {
	return caller.Err() == nil && errors.Is(exchange.Err(), context.DeadlineExceeded)
}

// ContextError keeps context.Canceled distinct from deadline expiry, which
// becomes ErrTimeout while still matching context.DeadlineExceeded.
func ContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrTimeout) {
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	}
	return err
}
