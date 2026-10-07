// config loads the edge agent's deployment settings from the environment.
package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultQueuePath              = "/var/lib/ambagrid-edge/queue.db"
	defaultQueueMaxBytes          = int64(512 << 20)
	defaultMaxEventBytes          = int64(1 << 20)
	defaultWarningPercent         = 70
	defaultCriticalReservePercent = 10
	defaultMinFilesystemFreeBytes = int64(256 << 20)
	defaultHealthAddr             = ":2112"
	defaultCollectorEnabled       = false
	defaultCollectorInterval      = 5 * time.Second
	defaultRegion                 = "africa-west"
)

type Config struct {
	Queue      QueueConfig
	Collector  CollectorConfig
	Uploader   UploaderConfig
	SMS        SMSConfig
	HealthAddr string
}

type SMSConfig struct {
	Enabled          bool
	Destination      string
	ModemID          string
	PollInterval     time.Duration
	FailureThreshold int
	OfflineAfter     time.Duration
	MaxAttempts      int
	HourlyLimit      int
	DailyLimit       int
	RetryInterval    time.Duration
	BatteryOverheatC float64
}

type CollectorConfig struct {
	Enabled  bool
	Region   string
	Interval time.Duration
}

type UploaderConfig struct {
	Enabled       bool
	Endpoint      string
	APIKey        string
	BatchSize     int
	BatchMaxBytes int64
	PollInterval  time.Duration
	Timeout       time.Duration
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	MaxRetries    int
}

type QueueConfig struct {
	Path                   string
	SiteID                 string
	GatewayID              string
	MaxStorageBytes        int64
	MaxEventBytes          int64
	WarningPercent         int
	CriticalReservePercent int
	MinFilesystemFreeBytes int64
}

func FromEnv() (Config, error) {
	siteID := strings.TrimSpace(os.Getenv("EDGE_SITE_ID"))
	if siteID == "" {
		return Config{}, fmt.Errorf("EDGE_SITE_ID must not be empty")
	}
	gatewayID := strings.TrimSpace(os.Getenv("EDGE_GATEWAY_ID"))
	if gatewayID == "" {
		return Config{}, fmt.Errorf("EDGE_GATEWAY_ID must not be empty")
	}

	maxBytes, err := envInt64("EDGE_QUEUE_MAX_BYTES", defaultQueueMaxBytes)
	if err != nil {
		return Config{}, err
	}
	maxEventBytes, err := envInt64("EDGE_QUEUE_MAX_EVENT_BYTES", defaultMaxEventBytes)
	if err != nil {
		return Config{}, err
	}
	warningPercent, err := envInt("EDGE_QUEUE_WARNING_PERCENT", defaultWarningPercent)
	if err != nil {
		return Config{}, err
	}
	reservePercent, err := envInt("EDGE_QUEUE_CRITICAL_RESERVE_PERCENT", defaultCriticalReservePercent)
	if err != nil {
		return Config{}, err
	}
	minFreeBytes, err := envInt64("EDGE_MIN_FILESYSTEM_FREE_BYTES", defaultMinFilesystemFreeBytes)
	if err != nil {
		return Config{}, err
	}
	uploaderEnabled, err := envBool("EDGE_UPLOADER_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	uploaderBatchSize, err := envInt("EDGE_UPLOADER_BATCH_SIZE", 50)
	if err != nil {
		return Config{}, err
	}
	uploaderBatchMaxBytes, err := envInt64("EDGE_UPLOADER_BATCH_MAX_BYTES", 1<<20)
	if err != nil {
		return Config{}, err
	}
	uploaderPollInterval, err := envDuration("EDGE_UPLOADER_POLL_INTERVAL", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	uploaderTimeout, err := envDuration("EDGE_UPLOADER_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	uploaderBaseDelay, err := envDuration("EDGE_UPLOADER_BASE_DELAY", 1*time.Second)
	if err != nil {
		return Config{}, err
	}
	uploaderMaxDelay, err := envDuration("EDGE_UPLOADER_MAX_DELAY", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	uploaderMaxRetries, err := envInt("EDGE_UPLOADER_MAX_RETRIES", 20)
	if err != nil {
		return Config{}, err
	}
	smsEnabled, err := envBool("EDGE_SMS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	smsPollInterval, err := envDuration("EDGE_SMS_POLL_INTERVAL", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	smsOfflineAfter, err := envDuration("EDGE_SMS_OFFLINE_AFTER", 2*time.Minute)
	if err != nil {
		return Config{}, err
	}
	smsRetryInterval, err := envDuration("EDGE_SMS_RETRY_INTERVAL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	smsFailureThreshold, err := envInt("EDGE_SMS_FAILURE_THRESHOLD", 3)
	if err != nil {
		return Config{}, err
	}
	smsMaxAttempts, err := envInt("EDGE_SMS_MAX_ATTEMPTS", 3)
	if err != nil {
		return Config{}, err
	}
	smsHourlyLimit, err := envInt("EDGE_SMS_HOURLY_LIMIT", 6)
	if err != nil {
		return Config{}, err
	}
	smsDailyLimit, err := envInt("EDGE_SMS_DAILY_LIMIT", 20)
	if err != nil {
		return Config{}, err
	}
	batteryOverheatC, err := envFloat("EDGE_SMS_BATTERY_OVERHEAT_C", 55)
	if err != nil {
		return Config{}, err
	}

	collectorEnabled, err := envBool("EDGE_SIMULATOR_ENABLED", defaultCollectorEnabled)
	if err != nil {
		return Config{}, err
	}
	collectorInterval, err := envDuration("EDGE_COLLECTOR_INTERVAL", defaultCollectorInterval)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Queue: QueueConfig{
			Path:                   envString("EDGE_QUEUE_PATH", defaultQueuePath),
			SiteID:                 siteID,
			GatewayID:              gatewayID,
			MaxStorageBytes:        maxBytes,
			MaxEventBytes:          maxEventBytes,
			WarningPercent:         warningPercent,
			CriticalReservePercent: reservePercent,
			MinFilesystemFreeBytes: minFreeBytes,
		},
		Collector: CollectorConfig{
			Enabled:  collectorEnabled,
			Region:   envString("EDGE_REGION", defaultRegion),
			Interval: collectorInterval,
		},
		Uploader: UploaderConfig{
			Enabled:       uploaderEnabled,
			Endpoint:      envString("EDGE_UPLOADER_ENDPOINT", ""),
			APIKey:        os.Getenv("EDGE_UPLOADER_API_KEY"),
			BatchSize:     uploaderBatchSize,
			BatchMaxBytes: uploaderBatchMaxBytes,
			PollInterval:  uploaderPollInterval,
			Timeout:       uploaderTimeout,
			BaseDelay:     uploaderBaseDelay,
			MaxDelay:      uploaderMaxDelay,
			MaxRetries:    uploaderMaxRetries,
		},
		SMS: SMSConfig{
			Enabled: smsEnabled, Destination: envString("EDGE_SMS_DESTINATION", ""), ModemID: envString("EDGE_SMS_MODEM_ID", "0"),
			PollInterval: smsPollInterval, FailureThreshold: smsFailureThreshold, OfflineAfter: smsOfflineAfter,
			MaxAttempts: smsMaxAttempts, HourlyLimit: smsHourlyLimit, DailyLimit: smsDailyLimit,
			RetryInterval: smsRetryInterval, BatteryOverheatC: batteryOverheatC,
		},
		HealthAddr: envString("EDGE_HEALTH_ADDR", defaultHealthAddr),
	}

	if cfg.Uploader.Enabled {
		if cfg.Uploader.Endpoint == "" {
			return Config{}, fmt.Errorf("EDGE_UPLOADER_ENDPOINT must be set when uploader is enabled")
		}
		if cfg.Uploader.APIKey == "" {
			return Config{}, fmt.Errorf("EDGE_UPLOADER_API_KEY must be set when uploader is enabled")
		}
	}
	if cfg.SMS.Enabled && cfg.SMS.Destination == "" {
		return Config{}, fmt.Errorf("EDGE_SMS_DESTINATION must be set when SMS fallback is enabled")
	}
	if cfg.SMS.FailureThreshold < 1 || cfg.SMS.MaxAttempts < 1 || cfg.SMS.HourlyLimit < 1 || cfg.SMS.DailyLimit < 1 || cfg.SMS.BatteryOverheatC <= 0 {
		return Config{}, fmt.Errorf("SMS fallback thresholds and limits must be positive")
	}

	return cfg, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%s must be finite", key)
	}
	return value, nil
}

func envBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
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
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return value, nil
}

func envString(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return value, nil
}

func envInt64(key string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return value, nil
}
