// Package config reads the ingestion bridge's deploy-time wiring from the
// environment, keeping the container image portable across local Compose and
// future deployments.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ingestion-go/internal/telemetry"
)

// holds deploy-time wiring for the MQTT edge and the Redpanda stream.
type Config struct {
	MQTTBroker             string
	MQTTTopicFilter        string
	MQTTClientID           string
	MQTTQOS                byte
	KafkaBrokers           []string
	KafkaTopic             string
	KafkaDLQTopic          string
	QueueSize              int
	WorkerCount            int
	ProduceTimeout         time.Duration
	ShutdownTimeout        time.Duration
	AllowAutoTopicCreation bool
	LogEveryNRecords       int64
}

// FromEnv builds a Config from environment variables, applying defaults and
// failing on any variable that's set but not parseable as its expected type.
func FromEnv() (Config, error) {
	mqttQOS, err := envByte("MQTT_QOS", 1)
	if err != nil {
		return Config{}, err
	}
	if mqttQOS > 2 {
		return Config{}, fmt.Errorf("MQTT_QOS must be 0, 1, or 2")
	}

	queueSize, err := envInt("INGESTION_QUEUE_SIZE", 1000)
	if err != nil {
		return Config{}, err
	}
	workerCount, err := envInt("INGESTION_WORKERS", 4)
	if err != nil {
		return Config{}, err
	}
	logEveryN, err := envInt("INGESTION_LOG_EVERY_N", 100)
	if err != nil {
		return Config{}, err
	}
	produceTimeout, err := envDuration("KAFKA_PRODUCE_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := envDuration("INGESTION_SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	allowAutoTopic, err := envBool("KAFKA_ALLOW_AUTO_TOPIC", true)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		MQTTBroker:             envString("MQTT_BROKER", "tcp://localhost:1883"),
		MQTTTopicFilter:        envString("MQTT_TOPIC_FILTER", "africa-west/+/smartmeter/+/telemetry"),
		MQTTClientID:           envString("MQTT_CLIENT_ID", defaultMQTTClientID()),
		MQTTQOS:                mqttQOS,
		KafkaBrokers:           envCSV("KAFKA_BROKERS", "localhost:9092"),
		KafkaTopic:             envString("KAFKA_TOPIC", "telemetry.ingested"),
		KafkaDLQTopic:          envString("KAFKA_DLQ_TOPIC", "telemetry.ingested.dlq"),
		QueueSize:              queueSize,
		WorkerCount:            workerCount,
		ProduceTimeout:         produceTimeout,
		ShutdownTimeout:        shutdownTimeout,
		AllowAutoTopicCreation: allowAutoTopic,
		LogEveryNRecords:       int64(logEveryN),
	}

	if cfg.MQTTBroker == "" {
		return Config{}, fmt.Errorf("MQTT_BROKER must not be empty")
	}
	if cfg.MQTTTopicFilter == "" {
		return Config{}, fmt.Errorf("MQTT_TOPIC_FILTER must not be empty")
	}
	if err := telemetry.ValidateFilter(cfg.MQTTTopicFilter); err != nil {
		return Config{}, err
	}
	if cfg.KafkaTopic == "" {
		return Config{}, fmt.Errorf("KAFKA_TOPIC must not be empty")
	}
	if cfg.KafkaDLQTopic == "" {
		return Config{}, fmt.Errorf("KAFKA_DLQ_TOPIC must not be empty")
	}
	if len(cfg.KafkaBrokers) == 0 {
		return Config{}, fmt.Errorf("KAFKA_BROKERS must include at least one broker")
	}
	if cfg.QueueSize < 1 {
		return Config{}, fmt.Errorf("INGESTION_QUEUE_SIZE must be greater than zero")
	}
	if cfg.WorkerCount < 1 {
		return Config{}, fmt.Errorf("INGESTION_WORKERS must be greater than zero")
	}

	return cfg, nil
}

// defaultMQTTClientID keeps each replica's client ID unique so the broker
// doesn't disconnect one running instance in favor of another: MQTT requires
// a client ID be unique per broker, and the previous fixed default broke as
// soon as more than one replica connected.
func defaultMQTTClientID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("ambagrid-ingestion-go-%s-%d", host, os.Getpid())
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
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return value, nil
}

func envByte(key string, fallback byte) (byte, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return byte(value), nil
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
	return value, nil
}
