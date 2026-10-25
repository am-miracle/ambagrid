package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"edge-agent-go/internal/config"
)

func TestFromEnvBuildsEdgeQueueConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	t.Setenv("EDGE_SITE_ID", "ng-kaji-01")
	t.Setenv("EDGE_GATEWAY_ID", "gateway-01")
	t.Setenv("EDGE_QUEUE_PATH", path)
	t.Setenv("EDGE_QUEUE_MAX_BYTES", "10485760")
	t.Setenv("EDGE_QUEUE_MAX_EVENT_BYTES", "262144")
	t.Setenv("EDGE_QUEUE_WARNING_PERCENT", "65")
	t.Setenv("EDGE_QUEUE_CRITICAL_RESERVE_PERCENT", "15")
	t.Setenv("EDGE_MIN_FILESYSTEM_FREE_BYTES", "1048576")
	t.Setenv("EDGE_SIMULATOR_ENABLED", "true")
	t.Setenv("EDGE_SIMULATOR_FAULTS", "true")
	t.Setenv("EDGE_COLLECTOR_INTERVAL", "2s")
	t.Setenv("EDGE_REGION", "africa-east")
	t.Setenv("EDGE_SMS_DEBOUNCE", "45s")
	t.Setenv("EDGE_HEALTH_ADDR", "127.0.0.1:32112")

	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Queue.Path != path || cfg.Queue.SiteID != "ng-kaji-01" || cfg.Queue.GatewayID != "gateway-01" {
		t.Fatalf("queue identity config = %#v", cfg.Queue)
	}
	if cfg.Queue.MaxStorageBytes != 10_485_760 || cfg.Queue.MaxEventBytes != 262_144 || cfg.Queue.WarningPercent != 65 || cfg.Queue.CriticalReservePercent != 15 {
		t.Fatalf("queue capacity config = %#v", cfg.Queue)
	}
	if cfg.Queue.MinFilesystemFreeBytes != 1_048_576 || cfg.HealthAddr != "127.0.0.1:32112" {
		t.Fatalf("service config = %#v", cfg)
	}
	if !cfg.Collector.Enabled || !cfg.Collector.Faults || cfg.Collector.Interval != 2*time.Second || cfg.Collector.Region != "africa-east" {
		t.Fatalf("collector config = %#v", cfg.Collector)
	}
	if cfg.SMS.Debounce != 45*time.Second {
		t.Fatalf("SMS debounce = %v, want 45s", cfg.SMS.Debounce)
	}
	if cfg.SMS.RetryInterval != 7*time.Minute+30*time.Second {
		t.Fatalf("SMS retry interval = %v, want 7m30s", cfg.SMS.RetryInterval)
	}
}

func TestFromEnvRequiresStableEdgeIdentity(t *testing.T) {
	t.Setenv("EDGE_SITE_ID", "")
	t.Setenv("EDGE_GATEWAY_ID", "")

	if _, err := config.FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want missing identity error")
	}
}

func TestFromEnvRejectsNonFiniteBatteryThreshold(t *testing.T) {
	t.Setenv("EDGE_SITE_ID", "site-01")
	t.Setenv("EDGE_GATEWAY_ID", "gateway-01")
	t.Setenv("EDGE_SMS_BATTERY_OVERHEAT_C", "NaN")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("FromEnv() accepted NaN battery threshold")
	}
}

func TestFromEnvRejectsSMSAttemptPolicyOutsideFifteenMinuteWindow(t *testing.T) {
	t.Setenv("EDGE_SITE_ID", "site-01")
	t.Setenv("EDGE_GATEWAY_ID", "gateway-01")
	t.Setenv("EDGE_SMS_MAX_ATTEMPTS", "3")
	t.Setenv("EDGE_SMS_RETRY_INTERVAL", "7m29s")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("FromEnv() accepted SMS retries that exhaust three attempts before 15 minutes")
	}

	t.Setenv("EDGE_SMS_RETRY_INTERVAL", "7m30s")
	t.Setenv("EDGE_SMS_MAX_ATTEMPTS", "2")
	if _, err := config.FromEnv(); err != nil {
		t.Fatalf("FromEnv() rejected a lower attempt limit with valid spacing: %v", err)
	}

	t.Setenv("EDGE_SMS_MAX_ATTEMPTS", "4")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("FromEnv() accepted more than three SMS attempts per incident")
	}
}

func TestFromEnvValidatesSMSHeartbeat(t *testing.T) {
	tests := map[string]map[string]string{
		"gateway ID not SMS-safe":  {"EDGE_GATEWAY_ID": "gateway.01"},
		"interval below offline":   {"EDGE_SMS_HEARTBEAT_INTERVAL": "1m"},
		"non-positive daily limit": {"EDGE_SMS_HEARTBEAT_DAILY_LIMIT": "0"},
		"interval beyond a day":    {"EDGE_SMS_HEARTBEAT_INTERVAL": "25h"},
		"fractional interval":      {"EDGE_SMS_HEARTBEAT_INTERVAL": "30m500ms"},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("EDGE_SITE_ID", "ng-kaji-01")
			t.Setenv("EDGE_GATEWAY_ID", "gateway-01")
			t.Setenv("EDGE_SMS_ENABLED", "true")
			t.Setenv("EDGE_SMS_DESTINATION", "+2348000000000")
			for key, value := range overrides {
				t.Setenv(key, value)
			}
			if _, err := config.FromEnv(); err == nil {
				t.Fatal("FromEnv() accepted an invalid SMS heartbeat configuration")
			}
		})
	}
}

func TestFromEnvConfiguresCommandLedgerRetention(t *testing.T) {
	t.Setenv("EDGE_SITE_ID", "ng-kaji-01")
	t.Setenv("EDGE_GATEWAY_ID", "gateway-01")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands.Retention != 30*24*time.Hour || cfg.Commands.PruneInterval != time.Hour {
		t.Fatalf("default command config = %+v", cfg.Commands)
	}

	t.Setenv("EDGE_COMMAND_RETENTION", "168h")
	t.Setenv("EDGE_COMMAND_PRUNE_INTERVAL", "10m")
	if cfg, err = config.FromEnv(); err != nil {
		t.Fatal(err)
	}
	if cfg.Commands.Retention != 168*time.Hour || cfg.Commands.PruneInterval != 10*time.Minute {
		t.Fatalf("configured command config = %+v", cfg.Commands)
	}

	t.Setenv("EDGE_COMMAND_RETENTION", "0s")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("accepted a zero command retention")
	}
}

func TestFromEnvConfiguresCommandDelivery(t *testing.T) {
	t.Setenv("EDGE_SITE_ID", "ng-kaji-01")
	t.Setenv("EDGE_GATEWAY_ID", "gateway-01")
	t.Setenv("EDGE_COMMANDS_ENABLED", "true")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("enabled command delivery without an endpoint")
	}

	t.Setenv("EDGE_COMMANDS_ENDPOINT", "http://api:8081")
	t.Setenv("EDGE_UPLOADER_API_KEY", "key-kaji")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("chose the simulator command adapter without the simulator running")
	}

	t.Setenv("EDGE_SIMULATOR_ENABLED", "true")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Commands
	if !c.Enabled || c.Endpoint != "http://api:8081" || c.APIKey != "key-kaji" || c.PollInterval != 15*time.Second || c.Timeout != 30*time.Second {
		t.Fatalf("command delivery config = %+v", c)
	}
	t.Setenv("EDGE_COMMANDS_API_KEY", "key-commands")
	if cfg, err = config.FromEnv(); err != nil || cfg.Commands.APIKey != "key-commands" {
		t.Fatalf("command key = %q, %v", cfg.Commands.APIKey, err)
	}
}

func TestFromEnvConfiguresMQTTCommandAdapter(t *testing.T) {
	t.Setenv("EDGE_SITE_ID", "ng-kaji-01")
	t.Setenv("EDGE_GATEWAY_ID", "gateway-01")
	t.Setenv("EDGE_COMMANDS_ENABLED", "true")
	t.Setenv("EDGE_COMMANDS_ENDPOINT", "http://api:8081")
	t.Setenv("EDGE_COMMANDS_API_KEY", "key-kaji")
	t.Setenv("EDGE_COMMAND_ADAPTER", "mqtt")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("accepted the mqtt adapter without a broker")
	}
	t.Setenv("EDGE_MQTT_BROKER", "tcp://localhost:1883")
	t.Setenv("EDGE_MQTT_CAPABILITIES_FILE", "/etc/ambagrid/meter.yaml")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Commands.MQTT
	if cfg.Commands.Adapter != "mqtt" || m.Broker != "tcp://localhost:1883" || m.ClientID != "ambagrid-edge-gateway-01" || m.TopicPrefix != "ambagrid/ng-kaji-01" {
		t.Fatalf("mqtt config = %+v", cfg.Commands)
	}
	t.Setenv("EDGE_COMMAND_ADAPTER", "modbus")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("accepted an unknown command adapter")
	}
}

func TestVictronRequiresBatteryServiceIdentity(t *testing.T) {
	t.Setenv("EDGE_SITE_ID", "test-site")
	t.Setenv("EDGE_GATEWAY_ID", "test-gateway")
	t.Setenv("EDGE_VICTRON_ADDRESS", "192.0.2.1:502")
	t.Setenv("EDGE_VICTRON_UNIT_ID", "")
	t.Setenv("EDGE_VICTRON_DEVICE_ID", "")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("expected missing Victron unit/device error")
	}
	t.Setenv("EDGE_VICTRON_UNIT_ID", "225")
	t.Setenv("EDGE_VICTRON_DEVICE_ID", "battery-01")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Victron.UnitID != 225 || cfg.Victron.DeviceID != "battery-01" {
		t.Fatalf("config=%+v", cfg.Victron)
	}
}
