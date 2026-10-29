package main

import (
	"context"
	"fmt"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/modbus"
	"edge-agent-go/internal/adapter/mqtt"
	"edge-agent-go/internal/adapter/simulator"
	"edge-agent-go/internal/collector"
	"edge-agent-go/internal/commands"
	"edge-agent-go/internal/config"
	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/health"
)

type hardware struct {
	sources  []collector.Source
	executor commands.Executor
	close    func()
}

type simulatorDevice struct {
	inner adapter.TelemetryAdapter
	id    string
}

func (s simulatorDevice) Read(ctx context.Context) ([]domain.Reading, error) {
	readings, err := s.inner.Read(ctx)
	if err != nil {
		return nil, err
	}
	for _, reading := range readings {
		if reading.DeviceID == s.id {
			return []domain.Reading{reading}, nil
		}
	}
	return nil, nil // an offline simulated device produces no fresh reading
}

// configureHardware owns startup cleanup and gives each command route its own
// ledger/capability checks. Shared MQTT transport subscriptions are device-specific.
func configureHardware(ctx context.Context, cfg config.Config, store adapter.LedgerStore, metrics *health.AdapterMetrics) (result hardware, err error) {
	var closers []func()
	result.close = func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	defer func() {
		if err != nil {
			result.close()
		}
	}()
	devices := cfg.Devices
	if cfg.DevicesFile == "" {
		if cfg.Collector.Enabled {
			for _, id := range []string{"meter-01", "battery-01", "inverter-01"} {
				devices = append(devices, config.DeviceConfig{ID: id, Telemetry: "simulator"})
			}
		}
		if cfg.Modbus.Address != "" {
			unit := cfg.Modbus.UnitID
			devices = append(devices, config.DeviceConfig{ID: cfg.Modbus.DeviceID, Telemetry: "modbus", Modbus: &config.DeviceModbusConfig{Address: cfg.Modbus.Address, UnitID: &unit, MapFile: cfg.Modbus.MapFile, Timeout: cfg.Modbus.Timeout}})
		}
	}
	var simulated *simulator.Adapter
	getSimulator := func() *simulator.Adapter {
		if simulated == nil {
			var options []simulator.Option
			if cfg.Collector.Faults {
				options = append(options, simulator.WithCriticalFaults())
			}
			simulated = simulator.New(cfg.Queue.SiteID, options...)
		}
		return simulated
	}
	var transport *mqtt.AutopahoTransport
	routes := map[string]adapter.CommandAdapter{}
	for _, device := range devices {
		var source adapter.TelemetryAdapter
		switch device.Telemetry {
		case "simulator":
			source = simulatorDevice{getSimulator(), device.ID}
		case "modbus":
			settings := device.ModbusSettings()
			registerMap, loadErr := modbus.LoadMap(settings.MapFile)
			if loadErr != nil {
				return result, fmt.Errorf("device %q register map: %w", device.ID, loadErr)
			}
			client := modbus.NewTCPClient(settings.Address, settings.Timeout)
			closers = append(closers, func() { _ = client.Close() })
			source, err = modbus.New(modbus.Config{Map: registerMap, Device: modbus.Device{SiteID: cfg.Queue.SiteID, DeviceID: device.ID}, UnitID: settings.UnitID}, client)
			if err != nil {
				return result, fmt.Errorf("device %q: %w", device.ID, err)
			}
		}
		if source != nil {
			result.sources = append(result.sources, metrics.Telemetry(device.Telemetry, device.ID, source))
		}
		if !cfg.Commands.Enabled || device.Command == "" {
			continue
		}
		var command adapter.CommandAdapter
		switch device.Command {
		case "simulator":
			command = getSimulator()
		case "mqtt":
			caps, loadErr := adapter.LoadCapabilities(device.CapabilitiesFile)
			if loadErr != nil {
				return result, fmt.Errorf("device %q capabilities: %w", device.ID, loadErr)
			}
			if transport == nil {
				transport, err = dialCommandMQTT(ctx, cfg.Commands.MQTT)
				if err != nil {
					return result, err
				}
				closers = append(closers, transport.Close)
			}
			topics, topicErr := mqtt.NewTopics(cfg.Commands.MQTT.CommandTopic, cfg.Commands.MQTT.ReplyTopic, cfg.Queue.SiteID)
			if topicErr != nil {
				return result, topicErr
			}
			subscribeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			command, err = mqtt.New(subscribeCtx, mqtt.Config{DeviceID: device.ID, Topics: topics, Capabilities: caps, ResponseTimeout: cfg.Commands.MQTT.ResponseTimeout}, transport)
			cancel()
			if err != nil {
				return result, fmt.Errorf("device %q MQTT: %w", device.ID, err)
			}
		}
		routes[device.ID] = metrics.Command(device.Command, device.ID, adapter.NewLedger(store, command))
	}
	if cfg.Commands.Enabled && cfg.DevicesFile == "" {
		command, closeCommand, configureErr := newCommandAdapter(ctx, cfg, simulated)
		if configureErr != nil {
			return result, configureErr
		}
		closers = append(closers, closeCommand)
		result.executor = metrics.Command(cfg.Commands.Adapter, "all", adapter.NewLedger(store, command))
	} else {
		result.executor = adapter.NewRouter(routes)
	}
	return result, nil
}
