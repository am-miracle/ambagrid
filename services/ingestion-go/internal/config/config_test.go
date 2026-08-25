package config

import (
	"testing"
	"time"
)

// envKeys lists every variable FromEnv reads, so tests can start from a clean
// slate regardless of what's set in the ambient shell running `go test`.
var envKeys = []string{
	"MQTT_BROKER",
	"MQTT_TOPIC_FILTER",
	"MQTT_CLIENT_ID",
	"MQTT_QOS",
	"KAFKA_BROKERS",
	"KAFKA_TOPIC",
	"INGESTION_QUEUE_SIZE",
	"INGESTION_WORKERS",
	"KAFKA_PRODUCE_TIMEOUT",
	"INGESTION_SHUTDOWN_TIMEOUT",
	"KAFKA_ALLOW_AUTO_TOPIC",
	"INGESTION_LOG_EVERY_N",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range envKeys {
		t.Setenv(key, "")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	if cfg.MQTTBroker != "tcp://localhost:1883" {
		t.Errorf("MQTTBroker = %q, want default", cfg.MQTTBroker)
	}
	if cfg.MQTTTopicFilter != "africa-west/+/smartmeter/+/telemetry" {
		t.Errorf("MQTTTopicFilter = %q, want default", cfg.MQTTTopicFilter)
	}
	if cfg.MQTTClientID == "" {
		t.Error("MQTTClientID default must not be empty")
	}
	if cfg.MQTTQOS != 1 {
		t.Errorf("MQTTQOS = %d, want 1", cfg.MQTTQOS)
	}
	if len(cfg.KafkaBrokers) != 1 || cfg.KafkaBrokers[0] != "localhost:9092" {
		t.Errorf("KafkaBrokers = %v, want [localhost:9092]", cfg.KafkaBrokers)
	}
	if cfg.KafkaTopic != "telemetry.raw" {
		t.Errorf("KafkaTopic = %q, want telemetry.raw", cfg.KafkaTopic)
	}
	if cfg.QueueSize != 1000 {
		t.Errorf("QueueSize = %d, want 1000", cfg.QueueSize)
	}
	if cfg.WorkerCount != 4 {
		t.Errorf("WorkerCount = %d, want 4", cfg.WorkerCount)
	}
	if cfg.ProduceTimeout != 10*time.Second {
		t.Errorf("ProduceTimeout = %s, want 10s", cfg.ProduceTimeout)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 10s", cfg.ShutdownTimeout)
	}
	if !cfg.AllowAutoTopicCreation {
		t.Error("AllowAutoTopicCreation = false, want true")
	}
	if cfg.LogEveryNRecords != 100 {
		t.Errorf("LogEveryNRecords = %d, want 100", cfg.LogEveryNRecords)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("MQTT_BROKER", "tcp://broker.internal:1883")
	t.Setenv("MQTT_TOPIC_FILTER", "africa-west/+/smartmeter/+/telemetry")
	t.Setenv("MQTT_CLIENT_ID", "ingestion-test")
	t.Setenv("MQTT_QOS", "2")
	t.Setenv("KAFKA_BROKERS", "broker-a:9092, broker-b:9092")
	t.Setenv("KAFKA_TOPIC", "telemetry.custom")
	t.Setenv("INGESTION_QUEUE_SIZE", "500")
	t.Setenv("INGESTION_WORKERS", "8")
	t.Setenv("KAFKA_PRODUCE_TIMEOUT", "2s")
	t.Setenv("INGESTION_SHUTDOWN_TIMEOUT", "3s")
	t.Setenv("KAFKA_ALLOW_AUTO_TOPIC", "false")
	t.Setenv("INGESTION_LOG_EVERY_N", "50")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	if cfg.MQTTBroker != "tcp://broker.internal:1883" {
		t.Errorf("MQTTBroker = %q", cfg.MQTTBroker)
	}
	if cfg.MQTTClientID != "ingestion-test" {
		t.Errorf("MQTTClientID = %q", cfg.MQTTClientID)
	}
	if cfg.MQTTQOS != 2 {
		t.Errorf("MQTTQOS = %d, want 2", cfg.MQTTQOS)
	}
	if len(cfg.KafkaBrokers) != 2 || cfg.KafkaBrokers[0] != "broker-a:9092" || cfg.KafkaBrokers[1] != "broker-b:9092" {
		t.Errorf("KafkaBrokers = %v", cfg.KafkaBrokers)
	}
	if cfg.KafkaTopic != "telemetry.custom" {
		t.Errorf("KafkaTopic = %q", cfg.KafkaTopic)
	}
	if cfg.QueueSize != 500 {
		t.Errorf("QueueSize = %d", cfg.QueueSize)
	}
	if cfg.WorkerCount != 8 {
		t.Errorf("WorkerCount = %d", cfg.WorkerCount)
	}
	if cfg.ProduceTimeout != 2*time.Second {
		t.Errorf("ProduceTimeout = %s", cfg.ProduceTimeout)
	}
	if cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("ShutdownTimeout = %s", cfg.ShutdownTimeout)
	}
	if cfg.AllowAutoTopicCreation {
		t.Error("AllowAutoTopicCreation = true, want false")
	}
	if cfg.LogEveryNRecords != 50 {
		t.Errorf("LogEveryNRecords = %d", cfg.LogEveryNRecords)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"non-numeric MQTT_QOS", map[string]string{"MQTT_QOS": "not-a-number"}},
		{"out-of-range MQTT_QOS", map[string]string{"MQTT_QOS": "3"}},
		{"non-numeric INGESTION_QUEUE_SIZE", map[string]string{"INGESTION_QUEUE_SIZE": "abc"}},
		{"zero INGESTION_QUEUE_SIZE", map[string]string{"INGESTION_QUEUE_SIZE": "0"}},
		{"negative INGESTION_QUEUE_SIZE", map[string]string{"INGESTION_QUEUE_SIZE": "-1"}},
		{"non-numeric INGESTION_WORKERS", map[string]string{"INGESTION_WORKERS": "abc"}},
		{"zero INGESTION_WORKERS", map[string]string{"INGESTION_WORKERS": "0"}},
		{"non-numeric INGESTION_LOG_EVERY_N", map[string]string{"INGESTION_LOG_EVERY_N": "abc"}},
		{"malformed KAFKA_PRODUCE_TIMEOUT", map[string]string{"KAFKA_PRODUCE_TIMEOUT": "soon"}},
		{"malformed INGESTION_SHUTDOWN_TIMEOUT", map[string]string{"INGESTION_SHUTDOWN_TIMEOUT": "soon"}},
		{"malformed KAFKA_ALLOW_AUTO_TOPIC", map[string]string{"KAFKA_ALLOW_AUTO_TOPIC": "maybe"}},
		{"empty KAFKA_BROKERS list", map[string]string{"KAFKA_BROKERS": ", ,"}},
		{"short MQTT_TOPIC_FILTER", map[string]string{"MQTT_TOPIC_FILTER": "africa-west/ng-kaji-01/met-0101"}},
		{"empty MQTT_TOPIC_FILTER segment", map[string]string{"MQTT_TOPIC_FILTER": "africa-west//smartmeter/+/telemetry"}},
		{"wrong MQTT_TOPIC_FILTER suffix", map[string]string{"MQTT_TOPIC_FILTER": "africa-west/+/smartmeter/+/events"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			if _, err := FromEnv(); err == nil {
				t.Fatalf("FromEnv returned nil error for %s", tt.name)
			}
		})
	}
}
