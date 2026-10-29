// config loads the edge agent's deployment settings from the environment.
package config

import (
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"edge-agent-go/internal/fallback"
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
	// Must outlast the longest time upstream may retry one command ID, or a
	// late retry would find no record and apply the command again.
	defaultCommandRetention     = 30 * 24 * time.Hour
	defaultCommandPruneInterval = time.Hour
)

type Config struct {
	DevicesFile string
	Devices     []DeviceConfig
	Queue       QueueConfig
	Collector   CollectorConfig
	Modbus      ModbusConfig
	Uploader    UploaderConfig
	SMS         SMSConfig
	Commands    CommandConfig
	HealthAddr  string
}

type CommandConfig struct {
	Retention     time.Duration
	PruneInterval time.Duration

	Enabled      bool
	Endpoint     string
	APIKey       string
	PollInterval time.Duration
	Timeout      time.Duration
	// Adapter is "simulator" or "mqtt".
	Adapter string
	MQTT    MQTTConfig
}

type MQTTConfig struct {
	AllowInsecure    bool
	Broker           string
	ClientID         string
	Username         string
	Password         string
	CAFile           string
	CapabilitiesFile string
	// CommandTopic and ReplyTopic are templates with {site_id} and {device_id}.
	CommandTopic    string
	ReplyTopic      string
	ResponseTimeout time.Duration
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
	SendTimeout      time.Duration
	BatteryOverheatC float64
	Debounce         time.Duration

	HeartbeatEnabled    bool
	HeartbeatInterval   time.Duration
	HeartbeatDailyLimit int
}

// ModbusConfig reads one Modbus device over TCP. MapFile describes its
// registers, so a new device model needs a map rather than a code change.
type ModbusConfig struct {
	Address  string
	UnitID   byte
	DeviceID string
	MapFile  string
	Timeout  time.Duration
}

type CollectorConfig struct {
	Enabled  bool
	Faults   bool
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
	PurgeRetention         time.Duration
	PurgeInterval          time.Duration
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
	purgeRetention, err := envDuration("EDGE_QUEUE_PURGE_RETENTION", 1*time.Hour)
	if err != nil {
		return Config{}, err
	}
	purgeInterval, err := envDuration("EDGE_QUEUE_PURGE_INTERVAL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	commandRetention, err := envDuration("EDGE_COMMAND_RETENTION", defaultCommandRetention)
	if err != nil {
		return Config{}, err
	}
	commandPruneInterval, err := envDuration("EDGE_COMMAND_PRUNE_INTERVAL", defaultCommandPruneInterval)
	if err != nil {
		return Config{}, err
	}
	allowInsecureMQTT, err := envBool("EDGE_MQTT_ALLOW_INSECURE", false)
	if err != nil {
		return Config{}, err
	}
	mqttResponseTimeout, err := envDuration("EDGE_MQTT_RESPONSE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	commandsEnabled, err := envBool("EDGE_COMMANDS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	commandPollInterval, err := envDuration("EDGE_COMMANDS_POLL_INTERVAL", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	commandTimeout, err := envDuration("EDGE_COMMANDS_TIMEOUT", 30*time.Second)
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
	smsRetryInterval, err := envDuration("EDGE_SMS_RETRY_INTERVAL", 7*time.Minute+30*time.Second)
	if err != nil {
		return Config{}, err
	}
	smsSendTimeout, err := envDuration("EDGE_SMS_SEND_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	smsDebounce, err := envDuration("EDGE_SMS_DEBOUNCE", 30*time.Second)
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
	heartbeatEnabled, err := envBool("EDGE_SMS_HEARTBEAT_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	heartbeatInterval, err := envDuration("EDGE_SMS_HEARTBEAT_INTERVAL", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	heartbeatDailyLimit, err := envInt("EDGE_SMS_HEARTBEAT_DAILY_LIMIT", 48)
	if err != nil {
		return Config{}, err
	}

	collectorEnabled, err := envBool("EDGE_SIMULATOR_ENABLED", defaultCollectorEnabled)
	if err != nil {
		return Config{}, err
	}
	collectorFaults, err := envBool("EDGE_SIMULATOR_FAULTS", false)
	if err != nil {
		return Config{}, err
	}
	collectorInterval, err := envDuration("EDGE_COLLECTOR_INTERVAL", defaultCollectorInterval)
	if err != nil {
		return Config{}, err
	}

	modbusAddress := envString("EDGE_MODBUS_ADDRESS", "")
	modbusUnit, err := envInt("EDGE_MODBUS_UNIT_ID", -1)
	if err != nil {
		return Config{}, err
	}
	modbusTimeout, err := envDuration("EDGE_MODBUS_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	modbusDevice := envString("EDGE_MODBUS_DEVICE_ID", "")
	modbusMap := envString("EDGE_MODBUS_MAP_FILE", "")
	if modbusAddress != "" {
		if _, _, err := net.SplitHostPort(modbusAddress); err != nil {
			return Config{}, fmt.Errorf("EDGE_MODBUS_ADDRESS must be host:port: %w", err)
		}
		if modbusUnit < 0 || modbusUnit > 255 || modbusDevice == "" || modbusMap == "" {
			return Config{}, fmt.Errorf("EDGE_MODBUS_ADDRESS requires EDGE_MODBUS_UNIT_ID (0..255), EDGE_MODBUS_DEVICE_ID and EDGE_MODBUS_MAP_FILE")
		}
	}

	cfg := Config{
		Modbus: ModbusConfig{Address: modbusAddress, UnitID: byte(modbusUnit), DeviceID: modbusDevice, MapFile: modbusMap, Timeout: modbusTimeout},
		Queue: QueueConfig{
			Path:                   envString("EDGE_QUEUE_PATH", defaultQueuePath),
			SiteID:                 siteID,
			GatewayID:              gatewayID,
			MaxStorageBytes:        maxBytes,
			MaxEventBytes:          maxEventBytes,
			WarningPercent:         warningPercent,
			CriticalReservePercent: reservePercent,
			MinFilesystemFreeBytes: minFreeBytes,
			PurgeRetention:         purgeRetention,
			PurgeInterval:          purgeInterval,
		},
		Collector: CollectorConfig{
			Enabled:  collectorEnabled,
			Faults:   collectorFaults,
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
			RetryInterval: smsRetryInterval, SendTimeout: smsSendTimeout, BatteryOverheatC: batteryOverheatC, Debounce: smsDebounce,
			HeartbeatEnabled: heartbeatEnabled, HeartbeatInterval: heartbeatInterval, HeartbeatDailyLimit: heartbeatDailyLimit,
		},
		Commands: CommandConfig{
			Retention: commandRetention, PruneInterval: commandPruneInterval,
			Enabled: commandsEnabled, Endpoint: envString("EDGE_COMMANDS_ENDPOINT", ""),
			// One site key authenticates both telemetry upload and command delivery.
			APIKey:       envString("EDGE_COMMANDS_API_KEY", os.Getenv("EDGE_UPLOADER_API_KEY")),
			PollInterval: commandPollInterval, Timeout: commandTimeout,
			Adapter: envString("EDGE_COMMAND_ADAPTER", "simulator"),
			MQTT: MQTTConfig{AllowInsecure: allowInsecureMQTT,
				Broker:           envString("EDGE_MQTT_BROKER", ""),
				ClientID:         envString("EDGE_MQTT_CLIENT_ID", "ambagrid-edge-"+gatewayID),
				Username:         envString("EDGE_MQTT_USERNAME", ""),
				Password:         os.Getenv("EDGE_MQTT_PASSWORD"),
				CAFile:           envString("EDGE_MQTT_CA_FILE", ""),
				CapabilitiesFile: envString("EDGE_MQTT_CAPABILITIES_FILE", ""),
				CommandTopic:     envString("EDGE_MQTT_COMMAND_TOPIC", "site/{site_id}/meter/{device_id}/cmd"),
				ReplyTopic:       envString("EDGE_MQTT_REPLY_TOPIC", "site/{site_id}/meter/{device_id}/cmd/reply"),
				ResponseTimeout:  mqttResponseTimeout,
			},
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
	if cfg.Commands.Enabled {
		if cfg.Commands.Endpoint == "" || cfg.Commands.APIKey == "" {
			return Config{}, fmt.Errorf("EDGE_COMMANDS_ENDPOINT and EDGE_COMMANDS_API_KEY must be set when command delivery is enabled")
		}
	}
	cfg.DevicesFile = envString("EDGE_DEVICES_FILE", "")
	if cfg.DevicesFile != "" {
		if cfg.Collector.Enabled || cfg.Modbus.Address != "" || os.Getenv("EDGE_COMMAND_ADAPTER") != "" || cfg.Commands.MQTT.CapabilitiesFile != "" {
			return Config{}, fmt.Errorf("EDGE_DEVICES_FILE replaces EDGE_SIMULATOR_ENABLED, EDGE_MODBUS_ADDRESS, EDGE_COMMAND_ADAPTER and EDGE_MQTT_CAPABILITIES_FILE")
		}
		cfg.Devices, err = loadDevices(cfg.DevicesFile)
		if err != nil {
			return Config{}, err
		}
	}
	if cfg.Commands.Enabled {
		selected := []string{cfg.Commands.Adapter}
		if cfg.DevicesFile != "" {
			selected = nil
			for _, d := range cfg.Devices {
				if d.Command != "" {
					selected = append(selected, d.Command)
				}
			}
			if len(selected) == 0 {
				return Config{}, fmt.Errorf("command delivery enabled but inventory has no command adapters")
			}
		}
		for _, selection := range selected {
			switch selection {
			case "simulator":
				if cfg.DevicesFile == "" && !cfg.Collector.Enabled {
					return Config{}, fmt.Errorf("EDGE_COMMAND_ADAPTER=simulator requires EDGE_SIMULATOR_ENABLED")
				}
			case "mqtt":
				if !cfg.Commands.MQTT.AllowInsecure && (!tlsBroker(cfg.Commands.MQTT.Broker) || cfg.Commands.MQTT.Username == "" || cfg.Commands.MQTT.Password == "" || cfg.Commands.MQTT.CAFile == "") {
					return Config{}, fmt.Errorf("production command MQTT requires a TLS broker URL (ssl://, tls://, mqtts://), credentials and a CA; EDGE_MQTT_ALLOW_INSECURE is for isolated development only")
				}
				if cfg.Commands.MQTT.Broker == "" || (cfg.DevicesFile == "" && cfg.Commands.MQTT.CapabilitiesFile == "") {
					return Config{}, fmt.Errorf("EDGE_COMMAND_ADAPTER=mqtt requires EDGE_MQTT_BROKER and EDGE_MQTT_CAPABILITIES_FILE")
				}
			default:
				return Config{}, fmt.Errorf("EDGE_COMMAND_ADAPTER must be simulator or mqtt, got %q", cfg.Commands.Adapter)
			}
		}
	}
	if cfg.SMS.Enabled && cfg.SMS.Destination == "" {
		return Config{}, fmt.Errorf("EDGE_SMS_DESTINATION must be set when SMS fallback is enabled")
	}
	if cfg.SMS.FailureThreshold < 1 || cfg.SMS.MaxAttempts < 1 || cfg.SMS.HourlyLimit < 1 || cfg.SMS.DailyLimit < 1 || cfg.SMS.BatteryOverheatC <= 0 || cfg.SMS.Debounce <= 0 {
		return Config{}, fmt.Errorf("SMS fallback thresholds and limits must be positive")
	}
	if cfg.SMS.MaxAttempts > fallback.MaxIncidentAttempts {
		return Config{}, fmt.Errorf("EDGE_SMS_MAX_ATTEMPTS must not exceed %d", fallback.MaxIncidentAttempts)
	}
	if cfg.SMS.HeartbeatDailyLimit < 1 {
		return Config{}, fmt.Errorf("EDGE_SMS_HEARTBEAT_DAILY_LIMIT must be positive")
	}
	if cfg.SMS.HeartbeatInterval < cfg.SMS.OfflineAfter {
		return Config{}, fmt.Errorf("EDGE_SMS_HEARTBEAT_INTERVAL must be at least EDGE_SMS_OFFLINE_AFTER")
	}
	if cfg.SMS.Enabled && cfg.SMS.HeartbeatEnabled {
		if _, err := fallback.FormatHeartbeat(fallback.Heartbeat{
			SiteID: siteID, GatewayID: gatewayID, Sequence: 1, Interval: cfg.SMS.HeartbeatInterval, SentAt: time.Unix(1, 0),
		}); err != nil {
			return Config{}, fmt.Errorf("EDGE_SITE_ID, EDGE_GATEWAY_ID, and EDGE_SMS_HEARTBEAT_INTERVAL must fit an SMS heartbeat: %w", err)
		}
	}
	minimumRetryInterval := fallback.MinimumRetryInterval(cfg.SMS.MaxAttempts)
	if cfg.SMS.RetryInterval < minimumRetryInterval {
		return Config{}, fmt.Errorf("EDGE_SMS_RETRY_INTERVAL must be at least %s for %d attempts over %s",
			minimumRetryInterval, cfg.SMS.MaxAttempts, fallback.IncidentAttemptWindow)
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

// tlsBroker reports whether the broker URL uses a scheme autopaho connects to over TLS.
func tlsBroker(broker string) bool {
	scheme, _, ok := strings.Cut(broker, "://")
	switch scheme {
	case "ssl", "tls", "mqtts", "mqtt+ssl", "tcps", "wss":
		return ok
	}
	return false
}
