package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/twmb/franz-go/pkg/kgo"
)

// holds deploy-time wiring for the MQTT edge and the Redpanda stream.
type Config struct {
	MQTTBroker             string
	MQTTTopicFilter        string
	MQTTClientID           string
	MQTTQOS                byte
	KafkaBrokers           []string
	KafkaTopic             string
	QueueSize              int
	WorkerCount            int
	ProduceTimeout         time.Duration
	ShutdownTimeout        time.Duration
	AllowAutoTopicCreation bool
	LogEveryNRecords       int64
}

// the handoff object between the Paho callback and Kafka workers.
type MQTTEnvelope struct {
	Topic    string
	Payload  []byte
	Metadata MQTTMessageMetadata
}

func main() {
	cfg, err := ConfigFromEnv()
	if err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := RunBridge(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("ingestion bridge stopped: %v", err)
	}
}

// keeps the container image portable across local Compose and future deployments.
func ConfigFromEnv() (Config, error) {
	mqttQOS, err := envByte("MQTT_QOS", 1)
	if err != nil {
		return Config{}, err
	}
	if mqttQOS > 2 {
		return Config{}, fmt.Errorf("MQTT_QOS must be 0, 1, or 2")
	}

	cfg := Config{
		MQTTBroker:             envString("MQTT_BROKER", "tcp://localhost:1883"),
		MQTTTopicFilter:        envString("MQTT_TOPIC_FILTER", "africa-west/+/smartmeter/+/telemetry"),
		MQTTClientID:           envString("MQTT_CLIENT_ID", "ambagrid-ingestion-go"),
		MQTTQOS:                mqttQOS,
		KafkaBrokers:           envCSV("KAFKA_BROKERS", "localhost:9092"),
		KafkaTopic:             envString("KAFKA_TOPIC", "telemetry.raw"),
		QueueSize:              envInt("INGESTION_QUEUE_SIZE", 1000),
		WorkerCount:            envInt("INGESTION_WORKERS", 4),
		ProduceTimeout:         envDuration("KAFKA_PRODUCE_TIMEOUT", 10*time.Second),
		ShutdownTimeout:        envDuration("INGESTION_SHUTDOWN_TIMEOUT", 10*time.Second),
		AllowAutoTopicCreation: envBool("KAFKA_ALLOW_AUTO_TOPIC", true),
		LogEveryNRecords:       int64(envInt("INGESTION_LOG_EVERY_N", 100)),
	}

	if cfg.MQTTBroker == "" {
		return Config{}, fmt.Errorf("MQTT_BROKER must not be empty")
	}
	if cfg.MQTTTopicFilter == "" {
		return Config{}, fmt.Errorf("MQTT_TOPIC_FILTER must not be empty")
	}
	if cfg.KafkaTopic == "" {
		return Config{}, fmt.Errorf("KAFKA_TOPIC must not be empty")
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

// connects the MQTT subscription to a bounded worker pool that produces into Redpanda.
func RunBridge(ctx context.Context, cfg Config) error {
	kafkaOpts := []kgo.Opt{
		kgo.SeedBrokers(cfg.KafkaBrokers...),
		kgo.ClientID("ambagrid-ingestion-go"),
		kgo.RecordDeliveryTimeout(cfg.ProduceTimeout),
	}
	if cfg.AllowAutoTopicCreation {
		kafkaOpts = append(kafkaOpts, kgo.AllowAutoTopicCreation())
	}

	kafkaClient, err := kgo.NewClient(kafkaOpts...)
	if err != nil {
		return fmt.Errorf("create kafka client: %w", err)
	}
	defer kafkaClient.Close()

	// Keep MQTT callbacks non-blocking: Paho invokes handlers on its own delivery path,
	// so slow Kafka writes are isolated behind this bounded queue.
	queue := make(chan MQTTEnvelope, cfg.QueueSize)
	var wg sync.WaitGroup
	var stats IngestionStats
	for workerID := 1; workerID <= cfg.WorkerCount; workerID++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			runProducerWorker(workerID, cfg, kafkaClient, queue, &stats)
		}(workerID)
	}

	mqttClient := mqtt.NewClient(mqttClientOptions(cfg, queue, &stats))
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		close(queue)
		wg.Wait()
		return fmt.Errorf("connect mqtt broker %s: %w", cfg.MQTTBroker, token.Error())
	}
	defer mqttClient.Disconnect(250)

	if token := mqttClient.Subscribe(cfg.MQTTTopicFilter, cfg.MQTTQOS, nil); token.Wait() && token.Error() != nil {
		close(queue)
		wg.Wait()
		return fmt.Errorf("subscribe mqtt topic %s: %w", cfg.MQTTTopicFilter, token.Error())
	}

	log.Printf("ingestion bridge running: mqtt=%s filter=%s kafka=%s topic=%s workers=%d", cfg.MQTTBroker, cfg.MQTTTopicFilter, strings.Join(cfg.KafkaBrokers, ","), cfg.KafkaTopic, cfg.WorkerCount)
	<-ctx.Done()

	// Stop accepting new MQTT deliveries first, then drain the queued messages to Kafka.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if token := mqttClient.Unsubscribe(cfg.MQTTTopicFilter); token.Wait() && token.Error() != nil {
			log.Printf("mqtt unsubscribe failed: %v", token.Error())
		}
		close(queue)
		wg.Wait()
	}()

	select {
	case <-shutdownCtx.Done():
		return fmt.Errorf("shutdown timeout: %w", shutdownCtx.Err())
	case <-done:
		log.Printf("ingestion bridge stopped: received=%d produced=%d dropped=%d failed=%d", stats.Received(), stats.Produced(), stats.Dropped(), stats.Failed())
		return ctx.Err()
	}
}

func mqttClientOptions(cfg Config, queue chan<- MQTTEnvelope, stats *IngestionStats) *mqtt.ClientOptions {
	return mqtt.NewClientOptions().
		AddBroker(cfg.MQTTBroker).
		SetClientID(cfg.MQTTClientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectTimeout(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
			envelope := MQTTEnvelope{
				Topic:   msg.Topic(),
				Payload: append([]byte(nil), msg.Payload()...),
				Metadata: MQTTMessageMetadata{
					Duplicate: msg.Duplicate(),
					QOS:       msg.Qos(),
					Retained:  msg.Retained(),
					MessageID: msg.MessageID(),
				},
			}
			stats.AddReceived()
			// Dropping under sustained backpressure is explicit for now; the raw hardware
			// stream is high-volume, and blocking here would stall MQTT client delivery.
			select {
			case queue <- envelope:
			default:
				stats.AddDropped()
				log.Printf("ingestion queue full; dropped mqtt message topic=%s", msg.Topic())
			}
		})
}

func runProducerWorker(workerID int, cfg Config, kafkaClient *kgo.Client, queue <-chan MQTTEnvelope, stats *IngestionStats) {
	for envelope := range queue {
		record, err := BuildTelemetryRecord(cfg.KafkaTopic, envelope.Topic, envelope.Payload, envelope.Metadata)
		if err != nil {
			stats.AddFailed()
			log.Printf("worker=%d invalid mqtt message topic=%s err=%v", workerID, envelope.Topic, err)
			continue
		}

		// Use a fresh background timeout so shutdown can drain already accepted messages.
		produceCtx, cancel := context.WithTimeout(context.Background(), cfg.ProduceTimeout)
		err = kafkaClient.ProduceSync(produceCtx, record).FirstErr()
		cancel()
		if err != nil {
			stats.AddFailed()
			log.Printf("worker=%d kafka produce failed topic=%s key=%s err=%v", workerID, record.Topic, string(record.Key), err)
			continue
		}

		produced := stats.AddProduced()
		if cfg.LogEveryNRecords > 0 && produced%cfg.LogEveryNRecords == 0 {
			log.Printf("worker=%d produced=%d kafka_topic=%s", workerID, produced, cfg.KafkaTopic)
		}
	}
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

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("invalid integer env %s=%q; using %d", key, raw, fallback)
		return fallback
	}
	return value
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

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("invalid bool env %s=%q; using %t", key, raw, fallback)
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid duration env %s=%q; using %s", key, raw, fallback)
		return fallback
	}
	return value
}
