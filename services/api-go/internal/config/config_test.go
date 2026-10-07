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
	if cfg.SiteOfflineAfter != 2*time.Minute || cfg.SiteEventStaleAfter != 2*time.Minute {
		t.Fatalf("site health thresholds = %s/%s", cfg.SiteOfflineAfter, cfg.SiteEventStaleAfter)
	}
	if cfg.AlertResolvedTopic != "alert.resolved" {
		t.Fatalf("AlertResolvedTopic = %q", cfg.AlertResolvedTopic)
	}
	if cfg.PaymentConfirmedTopic != "payment.confirmed" {
		t.Fatalf("PaymentConfirmedTopic = %q", cfg.PaymentConfirmedTopic)
	}
	if cfg.CreditIssuedTopic != "credit.issued" {
		t.Fatalf("CreditIssuedTopic = %q", cfg.CreditIssuedTopic)
	}
	if cfg.MeterCommandRequestedTopic != "meter.command.requested" {
		t.Fatalf("MeterCommandRequestedTopic = %q", cfg.MeterCommandRequestedTopic)
	}
	if cfg.PaystackSecretKey != "" {
		t.Fatalf("PaystackSecretKey = %q, want empty", cfg.PaystackSecretKey)
	}
	if cfg.DevMode {
		t.Fatal("DevMode = true, want false by default")
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
	t.Setenv("PAYMENT_CONFIRMED_TOPIC", "custom.payment.confirmed")
	t.Setenv("CREDIT_ISSUED_TOPIC", "custom.credit.issued")
	t.Setenv("METER_COMMAND_REQUESTED_TOPIC", "custom.meter.command.requested")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_example")

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
	if cfg.PaymentConfirmedTopic != "custom.payment.confirmed" || cfg.CreditIssuedTopic != "custom.credit.issued" || cfg.MeterCommandRequestedTopic != "custom.meter.command.requested" {
		t.Fatalf("revenue topic overrides not applied: %+v", cfg)
	}
	if cfg.PaystackSecretKey != "sk_test_example" {
		t.Fatalf("PaystackSecretKey = %q", cfg.PaystackSecretKey)
	}
}

func TestFromEnvEnablesDevMode(t *testing.T) {
	setRequired(t)
	t.Setenv("API_DEV_MODE", "true")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if !cfg.DevMode {
		t.Fatal("DevMode = false, want true when API_DEV_MODE=true")
	}
}

func TestFromEnvRejectsUnusableValues(t *testing.T) {
	tests := map[string]struct{ key, value string }{
		"page size is words":        {key: "API_DEFAULT_PAGE_SIZE", value: "many"},
		"timeout is not a duration": {key: "API_REQUEST_TIMEOUT", value: "10"},
		"timeout is negative":       {key: "API_REQUEST_TIMEOUT", value: "-1s"},
		"pool cannot serve anyone":  {key: "DB_MAX_CONNS", value: "0"},
		"ceiling below the default": {key: "API_MAX_PAGE_SIZE", value: "10"},
		"alert topic is blank":      {key: "ALERT_RESOLVED_TOPIC", value: " "},
		"payment topic is blank":    {key: "PAYMENT_CONFIRMED_TOPIC", value: " "},
		"credit topic is blank":     {key: "CREDIT_ISSUED_TOPIC", value: " "},
		"command topic is blank":    {key: "METER_COMMAND_REQUESTED_TOPIC", value: " "},
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
