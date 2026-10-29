package modbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

// ErrRegisterUnavailable means the device has no such register; the map
// decides whether that point was optional.
var ErrRegisterUnavailable = errors.New("modbus register unavailable")

// Client is the Modbus transport. It returns one word per register requested.
type Client interface {
	ReadRegisters(ctx context.Context, unit byte, function Function, address, quantity uint16) ([]uint16, error)
}

type Config struct {
	Map    Map
	Device Device
	UnitID byte
	Now    func() time.Time
}

// Adapter is a telemetry-only adapter; it exposes no commands.
type Adapter struct {
	cfg    Config
	client Client
}

func New(cfg Config, client Client) (*Adapter, error) {
	if strings.TrimSpace(cfg.Device.SiteID) == "" || strings.TrimSpace(cfg.Device.DeviceID) == "" || client == nil {
		return nil, fmt.Errorf("modbus site, device and client are required")
	}
	if err := cfg.Map.validate(); err != nil {
		return nil, fmt.Errorf("register map: %w", err)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Adapter{cfg: cfg, client: client}, nil
}

func (a *Adapter) Capabilities() adapter.Capabilities {
	m := a.cfg.Map
	return adapter.Capabilities{DeviceType: m.DeviceType, Manufacturer: m.Manufacturer, Model: m.Model, Capabilities: adapter.CapabilitySet{Telemetry: true}}
}

// Read reads each point separately, because devices may lack the registers
// between them and fail a request that spans the gap.
func (a *Adapter) Read(ctx context.Context) ([]domain.Reading, error) {
	regs := Registers{}
	for _, p := range a.cfg.Map.Points {
		want := uint16(p.Type.Words())
		words, err := a.client.ReadRegisters(ctx, a.cfg.UnitID, p.Function, p.Address, want)
		if errors.Is(err, ErrRegisterUnavailable) {
			adapter.ReportUnsupportedField(ctx)
			continue
		}
		if err != nil {
			return nil, adapter.ContextError(fmt.Errorf("read %s register %d: %w", p.Field, p.Address, err))
		}
		if len(words) != int(want) {
			return nil, fmt.Errorf("%w: %s register %d returned %d words, want %d", adapter.ErrInvalidResponse, p.Field, p.Address, len(words), want)
		}
		if regs[p.Function] == nil {
			regs[p.Function] = map[uint16]uint16{}
		}
		for i, word := range words {
			regs[p.Function][p.Address+uint16(i)] = word
		}
	}
	reading, err := Decode(a.cfg.Map, regs, a.cfg.Device, a.cfg.Now())
	if err != nil {
		return nil, err
	}
	return []domain.Reading{reading}, nil
}
