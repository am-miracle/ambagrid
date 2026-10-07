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
	t.Setenv("EDGE_COLLECTOR_INTERVAL", "2s")
	t.Setenv("EDGE_REGION", "africa-east")
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
	if !cfg.Collector.Enabled || cfg.Collector.Interval != 2*time.Second || cfg.Collector.Region != "africa-east" {
		t.Fatalf("collector config = %#v", cfg.Collector)
	}
}

func TestFromEnvRequiresStableEdgeIdentity(t *testing.T) {
	t.Setenv("EDGE_SITE_ID", "")
	t.Setenv("EDGE_GATEWAY_ID", "")

	if _, err := config.FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want missing identity error")
	}
}
