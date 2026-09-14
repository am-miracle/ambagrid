// Tests configuration parsing, defaults, and validation.
package config

import (
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/ambagrid_operational")
}

func TestFromEnvFailsWithoutADatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() succeeded without DATABASE_URL")
	}
}

func TestFromEnvAppliesDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}

	if cfg.HTTPAddr != ":8081" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.DefaultPageSize != 50 || cfg.MaxPageSize != 200 {
		t.Fatalf("page sizes = %d/%d", cfg.DefaultPageSize, cfg.MaxPageSize)
	}
	if cfg.AlertResolvedTopic != "alert.resolved" {
		t.Fatalf("AlertResolvedTopic = %q", cfg.AlertResolvedTopic)
	}
	// Postgres must allow the API time to return its own timeout response.
	if cfg.DBStatementTimeout <= cfg.DBQueryTimeout {
		t.Fatalf("DBStatementTimeout %s must exceed DBQueryTimeout %s", cfg.DBStatementTimeout, cfg.DBQueryTimeout)
	}
}

func TestFromEnvReadsOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("API_HTTP_ADDR", "0.0.0.0:9000")
	t.Setenv("API_REQUEST_TIMEOUT", "15s")
	t.Setenv("DB_MAX_CONNS", "40")
	t.Setenv("API_CORS_ALLOWED_ORIGINS", " https://ops.example , ")
	t.Setenv("ALERT_RESOLVED_TOPIC", "custom.alert.resolved")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}

	if cfg.HTTPAddr != "0.0.0.0:9000" || cfg.RequestTimeout != 15*time.Second || cfg.DBMaxConns != 40 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if len(cfg.CORSAllowedOrigins) != 1 || cfg.CORSAllowedOrigins[0] != "https://ops.example" {
		t.Fatalf("CORSAllowedOrigins = %v, want the blank entry dropped", cfg.CORSAllowedOrigins)
	}
	if cfg.AlertResolvedTopic != "custom.alert.resolved" {
		t.Fatalf("AlertResolvedTopic = %q", cfg.AlertResolvedTopic)
	}
}

func TestFromEnvRejectsUnusableValues(t *testing.T) {
	tests := map[string]struct{ key, value string }{
		"page size is words":        {key: "API_DEFAULT_PAGE_SIZE", value: "many"},
		"timeout is not a duration": {key: "API_REQUEST_TIMEOUT", value: "10"},
		"timeout is negative":       {key: "API_REQUEST_TIMEOUT", value: "-1s"},
		"pool cannot serve anyone":  {key: "DB_MAX_CONNS", value: "0"},
		"ceiling below the default": {key: "API_MAX_PAGE_SIZE", value: "10"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(test.key, test.value)

			if _, err := FromEnv(); err == nil {
				t.Fatalf("FromEnv() accepted %s=%q", test.key, test.value)
			}
		})
	}
}

func TestFromEnvRejectsSubMillisecondStatementTimeout(t *testing.T) {
	setRequired(t)
	t.Setenv("DB_STATEMENT_TIMEOUT", "500us")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() accepted a statement timeout that Postgres would truncate to disabled")
	}
}

func TestFromEnvRequiresQueryTimeoutBelowStatementTimeout(t *testing.T) {
	setRequired(t)
	t.Setenv("DB_QUERY_TIMEOUT", "10s")
	t.Setenv("DB_STATEMENT_TIMEOUT", "3s")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() accepted DB_QUERY_TIMEOUT >= DB_STATEMENT_TIMEOUT")
	}
}
