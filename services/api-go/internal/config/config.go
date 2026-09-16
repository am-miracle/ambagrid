// loads and validates API configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr           string
	ReadHeaderTimeout  time.Duration
	RequestTimeout     time.Duration
	IdleTimeout        time.Duration
	ShutdownTimeout    time.Duration
	CORSAllowedOrigins []string

	DatabaseURL        string
	DBMaxConns         int32
	DBMinConns         int32
	DBQueryTimeout     time.Duration
	DBStatementTimeout time.Duration

	DefaultPageSize       int
	MaxPageSize           int
	GlobalHistoryPageSize int

	AlertResolvedTopic string
}

// FromEnv loads defaults and rejects missing or invalid values.
func FromEnv() (Config, error) {
	readHeaderTimeout, err := envDuration("API_READ_HEADER_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	requestTimeout, err := envDuration("API_REQUEST_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	idleTimeout, err := envDuration("API_IDLE_TIMEOUT", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := envDuration("API_SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxConns, err := envInt("DB_MAX_CONNS", 10)
	if err != nil {
		return Config{}, err
	}
	minConns, err := envInt("DB_MIN_CONNS", 1)
	if err != nil {
		return Config{}, err
	}
	// Covers both waiting for a connection and running the query.
	queryTimeout, err := envDuration("DB_QUERY_TIMEOUT", 3*time.Second)
	if err != nil {
		return Config{}, err
	}
	// Stops Postgres work after a client-side timeout.
	statementTimeout, err := envDuration("DB_STATEMENT_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	defaultPageSize, err := envInt("API_DEFAULT_PAGE_SIZE", 50)
	if err != nil {
		return Config{}, err
	}
	maxPageSize, err := envInt("API_MAX_PAGE_SIZE", 200)
	if err != nil {
		return Config{}, err
	}
	// Fixed page size for unscoped alert history.
	globalHistoryPageSize, err := envInt("API_GLOBAL_HISTORY_PAGE_SIZE", 25)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:              envString("API_HTTP_ADDR", ":8081"),
		ReadHeaderTimeout:     readHeaderTimeout,
		RequestTimeout:        requestTimeout,
		IdleTimeout:           idleTimeout,
		ShutdownTimeout:       shutdownTimeout,
		CORSAllowedOrigins:    envCSV("API_CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:5173"),
		DatabaseURL:           envString("DATABASE_URL", ""),
		DBMaxConns:            int32(maxConns),
		DBMinConns:            int32(minConns),
		DBQueryTimeout:        queryTimeout,
		DBStatementTimeout:    statementTimeout,
		DefaultPageSize:       defaultPageSize,
		MaxPageSize:           maxPageSize,
		GlobalHistoryPageSize: globalHistoryPageSize,
		AlertResolvedTopic:    envString("ALERT_RESOLVED_TOPIC", "alert.resolved"),
	}

	if cfg.HTTPAddr == "" {
		return Config{}, fmt.Errorf("API_HTTP_ADDR must not be empty")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must be set")
	}
	if cfg.DBMaxConns < 1 {
		return Config{}, fmt.Errorf("DB_MAX_CONNS must be greater than zero")
	}
	if cfg.DBMinConns < 0 || cfg.DBMinConns > cfg.DBMaxConns {
		return Config{}, fmt.Errorf("DB_MIN_CONNS must be between zero and DB_MAX_CONNS")
	}
	if cfg.DBStatementTimeout < time.Millisecond {
		return Config{}, fmt.Errorf("DB_STATEMENT_TIMEOUT must be at least 1ms")
	}
	if cfg.DBQueryTimeout >= cfg.DBStatementTimeout {
		return Config{}, fmt.Errorf("DB_QUERY_TIMEOUT (%s) must be less than DB_STATEMENT_TIMEOUT (%s)", cfg.DBQueryTimeout, cfg.DBStatementTimeout)
	}
	if cfg.DefaultPageSize < 1 {
		return Config{}, fmt.Errorf("API_DEFAULT_PAGE_SIZE must be greater than zero")
	}
	if cfg.MaxPageSize < cfg.DefaultPageSize {
		return Config{}, fmt.Errorf("API_MAX_PAGE_SIZE must be at least API_DEFAULT_PAGE_SIZE")
	}
	if cfg.GlobalHistoryPageSize < 1 || cfg.GlobalHistoryPageSize > cfg.MaxPageSize {
		return Config{}, fmt.Errorf("API_GLOBAL_HISTORY_PAGE_SIZE must be between one and API_MAX_PAGE_SIZE")
	}
	if cfg.AlertResolvedTopic == "" {
		return Config{}, fmt.Errorf("ALERT_RESOLVED_TOPIC must not be empty")
	}

	return cfg, nil
}

func envString(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envCSV(key, fallback string) []string {
	raw := envString(key, fallback)
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %q", key, raw)
	}
	return value, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 5s: %q", key, raw)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", key)
	}
	return value, nil
}
