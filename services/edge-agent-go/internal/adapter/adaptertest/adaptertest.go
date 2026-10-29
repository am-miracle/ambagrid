// adaptertest runs the behaviour every hardware adapter must share, in the
// style of golang.org/x/net/nettest: each implementation supplies a factory.
//
// Fixtures are read from the harness filesystem, normally testdata/<adapter>/:
//
//	raw_input.json             bytes the device returns for one telemetry poll
//	malformed_input.json       poll bytes the adapter must report as ErrInvalidResponse
//	normalized_telemetry.json  readings the adapter must produce from raw_input.json
//	command_request.json       an adapter.Command
//	vendor_payload.json        bytes the device must receive for command_request.json
//	error_response.json        a device reply the adapter must report as ErrDeviceRejected
package adaptertest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

const (
	promptly = time.Second
	// inFlightGrace bounds how long a broken adapter has to reach an idle-looking
	// device. It can only hide a bug, never fail a correct adapter.
	inFlightGrace = 100 * time.Millisecond
)

// TelemetryDevice is the test double behind a telemetry adapter. Devices can
// implement Stall, Hold and Arrivals by calling devicetest.Gate on every exchange.
type TelemetryDevice interface {
	// Inject makes the next poll return raw instead of live device output.
	Inject(raw []byte)
	// Stall makes the next exchange block until its context ends. The returned
	// channel closes once an exchange is blocked inside the stall.
	Stall() <-chan struct{}
}

// CommandDevice is the test double behind a command adapter.
type CommandDevice interface {
	// Reject makes the next command receive response without being applied.
	Reject(response []byte)
	Stall() <-chan struct{}
	// Hold makes the next exchange block until release is called; entered
	// closes once that exchange is blocked.
	Hold() (entered <-chan struct{}, release func())
	// Arrivals counts exchanges that reached the device.
	Arrivals() int
	// Received returns the vendor payloads the device has applied, in order.
	Received() [][]byte
}

type TelemetryHarness struct {
	Adapter  adapter.TelemetryAdapter
	Device   TelemetryDevice
	Fixtures fs.FS
}

// CommandHarness.Adapter must provide at-most-once delivery, normally by
// wrapping the vendor adapter in adapter.NewLedger.
type CommandHarness struct {
	Adapter  adapter.CommandAdapter
	Device   CommandDevice
	Fixtures fs.FS
}

// Factories must return an adapter with fresh device state on every call.
type (
	TelemetryFactory func(t *testing.T) TelemetryHarness
	CommandFactory   func(t *testing.T) CommandHarness
)

func RunTelemetry(t *testing.T, factory TelemetryFactory) {
	t.Run("NormalizesFixture", func(t *testing.T) { testNormalizesFixture(t, factory(t)) })
	t.Run("LiveReadingsAreNormalized", func(t *testing.T) { testLiveReadingsAreNormalized(t, factory(t)) })
	t.Run("MalformedResponse", func(t *testing.T) { testMalformedResponse(t, factory(t)) })
	t.Run("CancelledContext", func(t *testing.T) {
		h := factory(t)
		testCancelled(t, h.Device.Stall, func(ctx context.Context) error {
			_, err := h.Adapter.Read(ctx)
			return err
		})
	})
	t.Run("Timeout", func(t *testing.T) {
		h := factory(t)
		testTimeout(t, h.Device.Stall, func(ctx context.Context) error {
			_, err := h.Adapter.Read(ctx)
			return err
		})
	})
}

func RunCommand(t *testing.T, factory CommandFactory) {
	t.Run("DeclaresCapabilities", func(t *testing.T) { testDeclaresCapabilities(t, factory(t)) })
	t.Run("SendsVendorPayload", func(t *testing.T) { testSendsVendorPayload(t, factory(t)) })
	t.Run("RetryAppliesOnce", func(t *testing.T) { testRetryAppliesOnce(t, factory(t)) })
	t.Run("DeviceRejection", func(t *testing.T) { testDeviceRejection(t, factory(t)) })
	t.Run("Unsupported", func(t *testing.T) { testUnsupported(t, factory(t)) })
	t.Run("CallsBeforeSend", func(t *testing.T) { testCallsBeforeSend(t, factory(t)) })
	t.Run("CancelledContext", func(t *testing.T) {
		h := factory(t)
		cmd := probeCommand(t, h)
		testCancelled(t, h.Device.Stall, func(ctx context.Context) error {
			_, err := h.Adapter.Execute(ctx, cmd)
			return err
		})
	})
	t.Run("Timeout", func(t *testing.T) {
		h := factory(t)
		cmd := probeCommand(t, h)
		testTimeout(t, h.Device.Stall, func(ctx context.Context) error {
			_, err := h.Adapter.Execute(ctx, cmd)
			return err
		})
		if n := len(h.Device.Received()); n != 0 {
			t.Fatalf("device applied %d payloads from timed-out commands", n)
		}
	})
	t.Run("CancelledWhileQueued", func(t *testing.T) { testCancelledWhileQueued(t, factory(t)) })
	t.Run("DeadlineWhileQueued", func(t *testing.T) { testDeadlineWhileQueued(t, factory(t)) })
	t.Run("ConcurrentRetriesApplyOnce", func(t *testing.T) { testConcurrentRetriesApplyOnce(t, factory(t)) })
	t.Run("RetriesWhileInFlightApplyOnce", func(t *testing.T) { testRetriesWhileInFlightApplyOnce(t, factory(t)) })
}

type normalizedReading struct {
	DeviceID            string                    `json:"device_id"`
	AssetType           domain.AssetType          `json:"asset_type"`
	TakenAt             time.Time                 `json:"taken_at"`
	LastSeenAt          time.Time                 `json:"last_seen_at"`
	Metrics             *domain.ElectricalMetrics `json:"metrics,omitempty"`
	InternalTemperature *float64                  `json:"internal_temperature_c"`
	RelayClosed         bool                      `json:"relay_closed"`
	BatterySOCPct       *float64                  `json:"battery_soc_pct"`
	SolarIrradiance     float64                   `json:"solar_irradiance_wm2"`
	InverterFailed      bool                      `json:"inverter_failed"`
	InverterHardFault   bool                      `json:"inverter_hard_fault"`
	TamperDetected      bool                      `json:"tamper_detected"`
	SiteOutage          *bool                     `json:"site_outage,omitempty"`
}

func testNormalizesFixture(t *testing.T, h TelemetryHarness) {
	var want []normalizedReading
	readFixtureJSON(t, h.Fixtures, "normalized_telemetry.json", &want)
	h.Device.Inject(readFixture(t, h.Fixtures, "raw_input.json"))

	readings := read(t, h.Adapter)
	if len(readings) != len(want) {
		t.Fatalf("readings = %d, fixture expects %d", len(readings), len(want))
	}
	for i, r := range readings {
		checkReading(t, r)
		got := normalizedReading{
			DeviceID: r.DeviceID, AssetType: r.AssetType, TakenAt: r.TakenAt, LastSeenAt: r.LastSeenAt, Metrics: r.Metrics,
			InternalTemperature: r.InternalTemperature, RelayClosed: r.RelayClosed,
			BatterySOCPct: r.BatterySOCPct, SolarIrradiance: r.SolarIrradiance,
			InverterFailed: r.InverterFailed, InverterHardFault: r.InverterHardFault,
			TamperDetected: r.TamperDetected, SiteOutage: r.SiteOutage,
		}
		if !got.TakenAt.Equal(want[i].TakenAt) {
			t.Errorf("reading %d taken_at = %s, want %s", i, got.TakenAt, want[i].TakenAt)
		}
		if !got.LastSeenAt.Equal(want[i].LastSeenAt) {
			t.Errorf("reading %d last_seen_at = %s, want %s", i, got.LastSeenAt, want[i].LastSeenAt)
		}
		got.TakenAt, want[i].TakenAt = time.Time{}, time.Time{}
		got.LastSeenAt, want[i].LastSeenAt = time.Time{}, time.Time{}
		if !approxEqual(got, want[i]) {
			t.Errorf("reading %d =\n  %+v\nwant\n  %+v", i, describe(got), describe(want[i]))
		}
	}
}

func testMalformedResponse(t *testing.T, h TelemetryHarness) {
	h.Device.Inject(readFixture(t, h.Fixtures, "malformed_input.json"))
	_, err := h.Adapter.Read(withDeadline(t))
	if !errors.Is(err, adapter.ErrInvalidResponse) {
		t.Fatalf("error = %v, want ErrInvalidResponse", err)
	}
}

func testLiveReadingsAreNormalized(t *testing.T, h TelemetryHarness) {
	readings := read(t, h.Adapter)
	if len(readings) == 0 {
		t.Fatal("adapter returned no readings")
	}
	for _, r := range readings {
		checkReading(t, r)
	}
}

// checkReading catches the common unit mistakes: local or zero timestamps and
// values reported in milli-, deci- or kilo- scale instead of the domain unit.
func checkReading(t *testing.T, r domain.Reading) {
	t.Helper()
	id := r.DeviceID
	if id == "" {
		t.Error("reading has no device ID")
	}
	if !r.AssetType.Valid() {
		t.Errorf("%s asset type %q is invalid", id, r.AssetType)
	}
	if r.TakenAt.IsZero() {
		t.Errorf("%s timestamp is zero", id)
	} else if r.TakenAt.Location() != time.UTC {
		t.Errorf("%s timestamp %s is not UTC", id, r.TakenAt)
	}
	switch {
	case r.LastSeenAt.IsZero():
		t.Errorf("%s last seen time is zero", id)
	case r.LastSeenAt.Location() != time.UTC:
		t.Errorf("%s last seen time %s is not UTC", id, r.LastSeenAt)
	case r.LastSeenAt.After(r.TakenAt):
		t.Errorf("%s last seen %s is after its reading time %s", id, r.LastSeenAt, r.TakenAt)
	}
	if !json.Valid(r.Payload) {
		t.Errorf("%s payload is not valid JSON: %q", id, r.Payload)
	}
	inRange := func(name string, v, lo, hi float64) {
		if v < lo || v > hi || math.IsNaN(v) {
			t.Errorf("%s %s = %v, want within [%v, %v]", id, name, v, lo, hi)
		}
	}
	if r.InternalTemperature != nil {
		inRange("internal temperature °C", *r.InternalTemperature, -40, 125)
	}
	if r.BatterySOCPct != nil {
		inRange("battery SoC %", *r.BatterySOCPct, 0, 100)
	}
	inRange("solar irradiance W/m²", r.SolarIrradiance, 0, 1500)
	if m := r.Metrics; m != nil {
		optional := func(name string, v *float64, lo, hi float64) {
			if v != nil {
				inRange(name, *v, lo, hi)
			}
		}
		optional("voltage V", m.Voltage, 0, 1000)
		optional("current A", m.Current, -1000, 1000)
		optional("active power kW", m.ActivePower, -1000, 1000)
		optional("frequency Hz", m.Frequency, 40, 70)
		optional("total energy kWh", m.TotalKWh, 0, 1e9)
	}
}

func testDeclaresCapabilities(t *testing.T, h CommandHarness) {
	caps := h.Adapter.Capabilities()
	if !caps.DeviceType.Valid() || caps.Manufacturer == "" || caps.Model == "" {
		t.Fatalf("capability declaration is incomplete: %+v", caps)
	}
}

func testSendsVendorPayload(t *testing.T, h CommandHarness) {
	cmd := fixtureCommand(t, h)
	result, err := h.Adapter.Execute(withDeadline(t), cmd)
	if err != nil {
		t.Fatalf("execute fixture command: %v", err)
	}
	checkResult(t, cmd, result)
	received := h.Device.Received()
	if len(received) != 1 {
		t.Fatalf("device received %d payloads, want 1", len(received))
	}
	want := readFixture(t, h.Fixtures, "vendor_payload.json")
	if !jsonEqual(received[0], want) {
		t.Fatalf("vendor payload =\n  %s\nwant\n  %s", received[0], bytes.TrimSpace(want))
	}
}

func testRetryAppliesOnce(t *testing.T, h CommandHarness) {
	cmd := fixtureCommand(t, h)
	first, err := h.Adapter.Execute(withDeadline(t), cmd)
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	second, err := h.Adapter.Execute(withDeadline(t), cmd)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := len(h.Device.Received()); n != 1 {
		t.Fatalf("device applied command %q %d times, want once", cmd.ID, n)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("retry result = %+v, first result = %+v", second, first)
	}
}

func testDeviceRejection(t *testing.T, h CommandHarness) {
	cmd := fixtureCommand(t, h)
	h.Device.Reject(readFixture(t, h.Fixtures, "error_response.json"))
	_, err := h.Adapter.Execute(withDeadline(t), cmd)
	if !errors.Is(err, adapter.ErrDeviceRejected) {
		t.Fatalf("error = %v, want ErrDeviceRejected", err)
	}
	if n := len(h.Device.Received()); n != 0 {
		t.Fatalf("device applied %d payloads after rejecting the command", n)
	}
}

func testUnsupported(t *testing.T, h CommandHarness) {
	caps := h.Adapter.Capabilities()
	deviceID := commandFixture(t, h).DeviceID
	expectUnsupported := func(cmd adapter.Command) {
		t.Helper()
		_, err := h.Adapter.Execute(withDeadline(t), cmd)
		if !errors.Is(err, adapter.ErrUnsupported) {
			t.Errorf("%s: error = %v, want ErrUnsupported", cmd.ID, err)
		}
	}

	expectUnsupported(adapter.Command{ID: "unsupported-unknown", Type: "reboot_meter", DeviceID: deviceID})
	for _, typ := range adapter.CommandTypes {
		cmd := validCommand("unsupported-"+string(typ), typ, deviceID)
		if caps.Supports(cmd) != nil {
			expectUnsupported(cmd)
		}
	}
	if caps.Capabilities.PrepaidCredit && !caps.Capabilities.STSToken {
		expectUnsupported(adapter.Command{ID: "unsupported-sts", Type: adapter.CommandCredit, DeviceID: deviceID, STSToken: "12345678901234567890"})
	}
	if n := len(h.Device.Received()); n != 0 {
		t.Fatalf("device applied %d unsupported payloads", n)
	}
}

type sendCounter struct{ n atomic.Int32 }

func (c *sendCounter) hook() error {
	c.n.Add(1)
	return nil
}

var errSendRefused = errors.New("adaptertest: send refused")

// testCallsBeforeSend checks the hook the ledger relies on to tell an unsent
// command from one whose outcome is unknown.
func testCallsBeforeSend(t *testing.T, h CommandHarness) {
	cmd := probeCommand(t, h)
	before := h.Device.Arrivals()
	var calls, arrivalsAtHook atomic.Int32
	ctx := adapter.WithSendHook(withDeadline(t), func() error {
		calls.Add(1)
		arrivalsAtHook.Store(int32(h.Device.Arrivals()))
		return nil
	})
	if _, err := h.Adapter.Execute(ctx, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("BeforeSend called %d times, want once", n)
	}
	if int(arrivalsAtHook.Load()) != before {
		t.Fatal("device was contacted before BeforeSend")
	}

	refused := cmd
	refused.ID += "-refused"
	applied, arrivals := len(h.Device.Received()), h.Device.Arrivals()
	ctx = adapter.WithSendHook(withDeadline(t), func() error { return errSendRefused })
	if _, err := h.Adapter.Execute(ctx, refused); !errors.Is(err, errSendRefused) {
		t.Fatalf("error = %v, want the refusal from BeforeSend", err)
	}
	if h.Device.Arrivals() != arrivals || len(h.Device.Received()) != applied {
		t.Fatal("adapter contacted the device after BeforeSend refused")
	}

	var unsupported sendCounter
	ctx = adapter.WithSendHook(withDeadline(t), unsupported.hook)
	if _, err := h.Adapter.Execute(ctx, adapter.Command{ID: "unsupported-hook", Type: "reboot_meter", DeviceID: cmd.DeviceID}); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("unsupported command error = %v, want ErrUnsupported", err)
	}
	if unsupported.n.Load() != 0 {
		t.Fatal("an unsupported command called BeforeSend")
	}
}

func testCancelled(t *testing.T, stall func() <-chan struct{}, call func(context.Context) error) {
	t.Run("BeforeCall", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := within(t, func() error { return call(ctx) })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
	t.Run("DuringExchange", func(t *testing.T) {
		stall()
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		err := within(t, func() error { return call(ctx) })
		if !errors.Is(err, context.Canceled) || errors.Is(err, adapter.ErrTimeout) {
			t.Fatalf("error = %v, want context.Canceled and not ErrTimeout", err)
		}
	})
}

func testTimeout(t *testing.T, stall func() <-chan struct{}, call func(context.Context) error) {
	expectTimeout := func(t *testing.T, ctx context.Context) {
		t.Helper()
		err := within(t, func() error { return call(ctx) })
		if !errors.Is(err, adapter.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want ErrTimeout wrapping context.DeadlineExceeded", err)
		}
	}
	t.Run("ExpiredBeforeCall", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		expectTimeout(t, ctx)
	})
	t.Run("DuringExchange", func(t *testing.T) {
		stall()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		expectTimeout(t, ctx)
	})
}

// holdDevice parks cmd inside a stalled device exchange until the test ends,
// so a second command is known to start while the device is busy.
func holdDevice(t *testing.T, h CommandHarness, cmd adapter.Command) {
	t.Helper()
	entered := h.Device.Stall()
	ctx, release := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.Adapter.Execute(ctx, cmd)
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-time.After(promptly):
			t.Errorf("command %q holding the device did not return after release", cmd.ID)
		}
	})
	select {
	case <-entered:
	case <-done:
		t.Fatalf("command %q returned before reaching the stalled device exchange", cmd.ID)
	case <-time.After(promptly):
		t.Fatalf("command %q never reached the stalled device exchange", cmd.ID)
	}
}

// Adapters whose transport allows concurrent commands may finish the queued
// command successfully; serialized adapters must give up as soon as its context ends.
func testCancelledWhileQueued(t *testing.T, h CommandHarness) {
	holder := probeCommand(t, h)
	queued := holder
	queued.ID += "-queued"
	holdDevice(t, h, holder)
	var sends sendCounter

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	err := within(t, func() error {
		_, err := h.Adapter.Execute(adapter.WithSendHook(ctx, sends.hook), queued)
		return err
	})
	if err != nil && (!errors.Is(err, context.Canceled) || errors.Is(err, adapter.ErrTimeout)) {
		t.Fatalf("error = %v, want context.Canceled and not ErrTimeout", err)
	}
	if err != nil && sends.n.Load() != 0 {
		t.Fatal("a command that never left the queue called BeforeSend")
	}
}

func testDeadlineWhileQueued(t *testing.T, h CommandHarness) {
	holder := probeCommand(t, h)
	queued := holder
	queued.ID += "-queued"
	holdDevice(t, h, holder)
	var sends sendCounter

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := within(t, func() error {
		_, err := h.Adapter.Execute(adapter.WithSendHook(ctx, sends.hook), queued)
		return err
	})
	if err != nil && (!errors.Is(err, adapter.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded)) {
		t.Fatalf("error = %v, want ErrTimeout wrapping context.DeadlineExceeded", err)
	}
	if err != nil && sends.n.Load() != 0 {
		t.Fatal("a command that never left the queue called BeforeSend")
	}
}

func testConcurrentRetriesApplyOnce(t *testing.T, h CommandHarness) {
	cmd := probeCommand(t, h)
	ctx := withDeadline(t)
	const attempts = 8
	results := make([]adapter.CommandResult, attempts)
	errs := make([]error, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = h.Adapter.Execute(ctx, cmd)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if !reflect.DeepEqual(results[i], results[0]) {
			t.Fatalf("attempt %d result = %+v, attempt 0 result = %+v", i, results[i], results[0])
		}
	}
	if n := len(h.Device.Received()); n != 1 {
		t.Fatalf("device applied command %q %d times across concurrent retries, want once", cmd.ID, n)
	}
}

// testRetriesWhileInFlightApplyOnce forces the overlap that
// testConcurrentRetriesApplyOnce only hopes the scheduler produces: retries
// start while the first attempt is held inside the device.
func testRetriesWhileInFlightApplyOnce(t *testing.T, h CommandHarness) {
	cmd := probeCommand(t, h)
	ctx := withDeadline(t)
	const attempts = 8
	type outcome struct {
		result adapter.CommandResult
		err    error
	}
	outcomes := make(chan outcome, attempts)
	attempt := func() {
		result, err := h.Adapter.Execute(ctx, cmd)
		outcomes <- outcome{result, err}
	}
	arrivalsBefore := h.Device.Arrivals()
	entered, release := h.Device.Hold()
	t.Cleanup(release)

	go attempt()
	select {
	case <-entered:
	case o := <-outcomes:
		t.Fatalf("first attempt returned (%+v, %v) without reaching the held device", o.result, o.err)
	case <-time.After(promptly):
		t.Fatal("first attempt never reached the held device")
	}
	for range attempts - 1 {
		go attempt()
	}
	select {
	case o := <-outcomes:
		t.Fatalf("a retry returned (%+v, %v) while the first attempt was still in the device", o.result, o.err)
	case <-time.After(inFlightGrace):
	}
	if n := h.Device.Arrivals() - arrivalsBefore; n != 1 {
		t.Fatalf("%d exchanges reached the device while the first attempt was in flight, want 1", n)
	}

	release()
	var first adapter.CommandResult
	for i := range attempts {
		select {
		case o := <-outcomes:
			if o.err != nil {
				t.Fatalf("attempt returned error after release: %v", o.err)
			}
			if i == 0 {
				first = o.result
			} else if !reflect.DeepEqual(o.result, first) {
				t.Fatalf("attempt result = %+v, another attempt returned %+v", o.result, first)
			}
		case <-time.After(promptly):
			t.Fatalf("%d of %d attempts still running after the device was released", attempts-i, attempts)
		}
	}
	if n := len(h.Device.Received()); n != 1 {
		t.Fatalf("device applied command %q %d times, want once", cmd.ID, n)
	}
}

func within(t *testing.T, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(promptly):
		t.Fatalf("adapter did not return within %s of its context ending", promptly)
		return nil
	}
}

func read(t *testing.T, a adapter.TelemetryAdapter) []domain.Reading {
	t.Helper()
	readings, err := a.Read(withDeadline(t))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return readings
}

func checkResult(t *testing.T, cmd adapter.Command, result adapter.CommandResult) {
	t.Helper()
	if result.CommandID != cmd.ID {
		t.Errorf("result command ID = %q, want %q", result.CommandID, cmd.ID)
	}
	if result.AcknowledgedAt.IsZero() || result.AcknowledgedAt.Location() != time.UTC {
		t.Errorf("acknowledged at = %s, want a UTC timestamp", result.AcknowledgedAt)
	}
}

func commandFixture(t *testing.T, h CommandHarness) adapter.Command {
	t.Helper()
	var cmd adapter.Command
	readFixtureJSON(t, h.Fixtures, "command_request.json", &cmd)
	return cmd
}

func fixtureCommand(t *testing.T, h CommandHarness) adapter.Command {
	t.Helper()
	cmd := commandFixture(t, h)
	if err := h.Adapter.Capabilities().Supports(cmd); err != nil {
		t.Fatalf("provide a supported command fixture: %v", err)
	}
	return cmd
}

// probeCommand picks a declared command for cancellation, timeout and concurrent
// retry checks independently of the fixture's command type.
func probeCommand(t *testing.T, h CommandHarness) adapter.Command {
	t.Helper()
	deviceID := commandFixture(t, h).DeviceID
	for _, typ := range adapter.CommandTypes {
		cmd := validCommand("probe-"+string(typ), typ, deviceID)
		if h.Adapter.Capabilities().Supports(cmd) == nil {
			return cmd
		}
	}
	t.Skip("adapter declares no commands")
	return adapter.Command{}
}

func validCommand(id string, typ adapter.CommandType, deviceID string) adapter.Command {
	cmd := adapter.Command{ID: id, Type: typ, DeviceID: deviceID}
	if typ == adapter.CommandCredit {
		cmd.CreditKWh = 1
	}
	return cmd
}

func withDeadline(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func readFixture(t *testing.T, fsys fs.FS, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func readFixtureJSON(t *testing.T, fsys fs.FS, name string, v any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(readFixture(t, fsys, name)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// approxEqual compares readings after unit conversion, where exact float
// equality would fail on harmless rounding such as 2301 dV -> 230.1 V.
func approxEqual(a, b normalizedReading) bool {
	ma, mb := a.Metrics, b.Metrics
	a.Metrics, b.Metrics = nil, nil
	if !closePtr(a.InternalTemperature, b.InternalTemperature) || !closePtr(a.BatterySOCPct, b.BatterySOCPct) || !closeTo(a.SolarIrradiance, b.SolarIrradiance) {
		return false
	}
	a.InternalTemperature, b.InternalTemperature = nil, nil
	a.BatterySOCPct, b.BatterySOCPct = nil, nil
	a.SolarIrradiance, b.SolarIrradiance = 0, 0
	if !reflect.DeepEqual(a, b) {
		return false
	}
	if (ma == nil) != (mb == nil) {
		return false
	}
	if ma == nil {
		return true
	}
	return closePtr(ma.Voltage, mb.Voltage) && closePtr(ma.Current, mb.Current) &&
		closePtr(ma.ActivePower, mb.ActivePower) && closePtr(ma.Frequency, mb.Frequency) &&
		closePtr(ma.TotalKWh, mb.TotalKWh)
}

func closeTo(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

func closePtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return closeTo(*a, *b)
}

func describe(r normalizedReading) string {
	data, _ := json.Marshal(r)
	return string(data)
}
