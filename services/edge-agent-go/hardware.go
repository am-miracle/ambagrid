package main

import (
	"context"
	"fmt"

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

	telemetryFactories := map[config.AdapterKind]func(config.DeviceConfig) (adapter.TelemetryAdapter, error){
		config.Simulator: func(device config.DeviceConfig) (adapter.TelemetryAdapter, error) {
			return simulatorDevice{getSimulator(), device.ID}, nil
		},
		config.Modbus: func(device config.DeviceConfig) (adapter.TelemetryAdapter, error) {
			settings := device.ModbusSettings()
			registerMap, err := modbus.LoadMap(settings.MapFile)
			if err != nil {
				return nil, err
			}
			client := modbus.NewTCPClient(settings.Address, settings.Timeout)
			closers = append(closers, func() { _ = client.Close() })
			return modbus.New(modbus.Config{Map: registerMap, Device: modbus.Device{SiteID: cfg.Queue.SiteID, DeviceID: device.ID}, UnitID: settings.UnitID}, client)
		},
	}
	commandFactories := map[config.AdapterKind]func(config.DeviceConfig) (adapter.CommandAdapter, error){
		config.Simulator: func(config.DeviceConfig) (adapter.CommandAdapter, error) { return getSimulator(), nil },
		config.MQTT: func(device config.DeviceConfig) (adapter.CommandAdapter, error) {
			caps, err := adapter.LoadCapabilities(device.CapabilitiesFile)
			if err != nil {
				return nil, err
			}
			if transport == nil {
				transport, err = dialCommandMQTT(ctx, cfg.Commands.MQTT)
				if err != nil {
					return nil, err
				}
				closers = append(closers, transport.Close)
			}
			topics, err := mqtt.NewTopics(cfg.Commands.MQTT.CommandTopic, cfg.Commands.MQTT.ReplyTopic, cfg.Queue.SiteID)
			if err != nil {
				return nil, err
			}
			return subscribeCommandMQTT(ctx, mqtt.Config{DeviceID: device.ID, Topics: topics, Capabilities: caps, ResponseTimeout: cfg.Commands.MQTT.ResponseTimeout}, transport)
		},
	}
	routes := map[string]adapter.CommandAdapter{}
	for _, device := range cfg.Devices {
		if device.Telemetry != "" {
			factory, ok := telemetryFactories[device.Telemetry]
			if !ok {
				return result, fmt.Errorf("device %q: no telemetry factory for %q", device.ID, device.Telemetry)
			}
			source, err := factory(device)
			if err != nil {
				return result, fmt.Errorf("device %q telemetry: %w", device.ID, err)
			}
			result.sources = append(result.sources, metrics.Telemetry(string(device.Telemetry), device.ID, source))
		}
		if !cfg.Commands.Enabled || device.Command == "" {
			continue
		}
		factory, ok := commandFactories[device.Command]
		if !ok {
			return result, fmt.Errorf("device %q: no command factory for %q", device.ID, device.Command)
		}
		command, err := factory(device)
		if err != nil {
			return result, fmt.Errorf("device %q command: %w", device.ID, err)
		}
		routes[device.ID] = metrics.Command(string(device.Command), device.ID, adapter.NewLedger(store, command))
	}
	result.executor = adapter.NewRouter(routes)
	return result, nil
}
