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
