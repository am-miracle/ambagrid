package simulator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"edge-agent-go/internal/adapter/devicetest"
)

// frame is the simulator's vendor wire format. Like most meters and BMS
// registers it carries scaled integers, so the adapter has real unit work to do.
type frame struct {
	Device   string `json:"device"`
	Kind     string `json:"kind"`
	TsMs     int64  `json:"ts_ms"`
	SeenMs   *int64 `json:"seen_ms,omitempty"`
	VoltDV   *int64 `json:"v_dv,omitempty"`
	CurrMA   *int64 `json:"i_ma,omitempty"`
	PowerW   *int64 `json:"p_w,omitempty"`
	FreqCHz  *int64 `json:"f_chz,omitempty"`
	EnergyWh *int64 `json:"e_wh,omitempty"`
	TempDC   int64  `json:"temp_dc"`
	Relay    int    `json:"relay"`
	Tamper   int    `json:"tamper,omitempty"`
	SoCPm    *int64 `json:"soc_pm,omitempty"`
	IrrWm2   *int64 `json:"irr_wm2,omitempty"`
	Fault    int    `json:"fault,omitempty"`
	Outage   *int   `json:"outage,omitempty"`
}

const (
	meterID = "meter-01"

	kindMeter    = "meter"
	kindBMS      = "bms"
	kindInverter = "inverter"

	faultSoft = 1
	faultHard = 2
)

type vendorCommand struct {
	Op     string `json:"op"`
	Ref    string `json:"ref"`
	Device string `json:"device"`
	Wh     int64  `json:"wh,omitempty"`
	Closed *bool  `json:"closed,omitempty"`
}

type vendorResponse struct {
	Ref       string `json:"ref"`
	OK        bool   `json:"ok"`
	TsMs      int64  `json:"ts_ms,omitempty"`
	Relay     *int   `json:"relay,omitempty"`
	BalanceWh *int64 `json:"balance_wh,omitempty"`
	Tamper    *int   `json:"tamper,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"msg,omitempty"`
}

// Device simulates the hardware behind the adapter: a meter, a battery BMS and
// an inverter on one gateway. Inject, Reject, Stall and Hold script its next reply;
// SetTamper and SetMeterOnline change its state until called again.
type Device struct {
	devicetest.Gate
	devicetest.Recorder

	mu           sync.Mutex
	now          func() time.Time
	cycle        uint64
	faults       bool
	relay        bool
	tamper       bool
	balanceWh    int64
	meterOffline bool
	lastMeter    *frame
	injected     [][]byte
	rejection    []byte
}

func newDevice() *Device {
	return &Device{now: time.Now, relay: true}
}

func (d *Device) Inject(raw []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.injected = append(d.injected, raw)
}

func (d *Device) Reject(response []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rejection = response
}

func (d *Device) SetTamper(detected bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tamper = detected
}

// SetMeterOnline(false) models a meter the gateway can no longer reach: polls
// repeat its last frame with seen_ms frozen, and commands to it get no reply.
func (d *Device) SetMeterOnline(online bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.meterOffline = !online
}

func (d *Device) Poll(ctx context.Context) ([]byte, error) {
	if err := d.Enter(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.injected) > 0 {
		raw := d.injected[0]
		d.injected = d.injected[1:]
		return raw, nil
	}
	return json.Marshal(d.generate())
}

func (d *Device) Send(ctx context.Context, payload []byte) ([]byte, error) {
	if err := d.Enter(ctx); err != nil {
		return nil, err
	}
	if d.unreachable(payload) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rejection != nil {
		response := d.rejection
		d.rejection = nil
		return response, nil
	}
	var cmd vendorCommand
	if err := json.Unmarshal(payload, &cmd); err != nil {
		return json.Marshal(vendorResponse{Code: "E_FRAME", Message: err.Error()})
	}
	if cmd.Device != meterID {
		return json.Marshal(vendorResponse{Ref: cmd.Ref, Code: "E_UNKNOWN_DEVICE", Message: fmt.Sprintf("no device %q", cmd.Device)})
	}
	switch cmd.Op {
	case "credit":
		d.balanceWh += cmd.Wh
	case "relay":
		if cmd.Closed == nil {
			return json.Marshal(vendorResponse{Ref: cmd.Ref, Code: "E_FRAME", Message: "relay state missing"})
		}
		d.relay = *cmd.Closed
	case "status":
	default:
		return json.Marshal(vendorResponse{Ref: cmd.Ref, Code: "E_OP", Message: fmt.Sprintf("unknown op %q", cmd.Op)})
	}
	d.Record(payload)
	relay, balance, tamper := boolInt(d.relay), d.balanceWh, boolInt(d.tamper)
	return json.Marshal(vendorResponse{Ref: cmd.Ref, OK: true, TsMs: d.now().UnixMilli(), Relay: &relay, BalanceWh: &balance, Tamper: &tamper})
}

func (d *Device) unreachable(payload []byte) bool {
	var cmd vendorCommand
	if json.Unmarshal(payload, &cmd) != nil || cmd.Device != meterID {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.meterOffline
}

func (d *Device) generate() []frame {
	ts := d.now().UnixMilli()
	phase := float64(d.cycle) / 8
	d.cycle++
	voltage := 230 + 3*math.Sin(phase)
	current := 6 + math.Sin(phase/2)
	powerKW := voltage * current / 1000
	totalKWh := 1500 + float64(d.cycle)*powerKW/720

	meter := frame{
		Device: meterID, Kind: kindMeter, TsMs: ts, SeenMs: &ts,
		VoltDV: scaled(voltage, 10), CurrMA: scaled(current, 1000), PowerW: scaled(powerKW, 1000),
		FreqCHz: scaled(50+0.04*math.Sin(phase/3), 100), EnergyWh: scaled(totalKWh, 1000),
		TempDC: *scaled(36+math.Sin(phase), 10), Relay: boolInt(d.relay), Tamper: boolInt(d.tamper),
	}
	battery := frame{
		Device: "battery-01", Kind: kindBMS, TsMs: ts,
		TempDC: *scaled(31+math.Sin(phase/2), 10), Relay: 1, SoCPm: scaled(72+8*math.Sin(phase/4), 10),
	}
	inverter := frame{
		Device: "inverter-01", Kind: kindInverter, TsMs: ts,
		TempDC: *scaled(42+2*math.Sin(phase/2), 10), Relay: 1, IrrWm2: scaled(650+100*math.Sin(phase/3), 1),
	}
	if d.faults {
		outage := 1
		battery.TempDC = 682
		battery.Outage = &outage
		inverter.Fault = faultHard
	}
	switch {
	case !d.meterOffline:
		d.lastMeter = &meter
	case d.lastMeter != nil:
		meter = *d.lastMeter
		meter.TsMs = ts
	default:
		return []frame{battery, inverter}
	}
	return []frame{meter, battery, inverter}
}

func scaled(v, factor float64) *int64 {
	n := int64(math.Round(v * factor))
	return &n
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
