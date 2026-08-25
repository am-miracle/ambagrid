// Package bridge connects an MQTT subscription to a bounded worker pool that
// produces the messages into Redpanda/Kafka.
package bridge

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/twmb/franz-go/pkg/kgo"

	"ingestion-go/internal/config"
	"ingestion-go/internal/stats"
	"ingestion-go/internal/telemetry"
)

// handoff object between the Paho callback and Kafka workers.
type envelope struct {
	Topic    string
	Payload  []byte
	Metadata telemetry.MessageMetadata
}

type queueGuard struct {
	mu     sync.RWMutex
	queue  chan envelope
	closed bool
}

func newQueueGuard(size int) *queueGuard {
	return &queueGuard{queue: make(chan envelope, size)}
}

func (g *queueGuard) enqueue(e envelope) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return false
	}
	select {
	case g.queue <- e:
		return true
	default:
		return false
	}
}

func (g *queueGuard) closeOnce() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		g.closed = true
		close(g.queue)
	}
}

// Run wires the MQTT subscription to the Kafka producer pool and blocks until
// ctx is cancelled or a fatal setup error occurs.
func Run(ctx context.Context, cfg config.Config) error {
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

	// Keep MQTT callbacks non-blocking: Paho invokes handlers on its own delivery path,
	// so slow Kafka writes are isolated behind this bounded queue.
	queue := newQueueGuard(cfg.QueueSize)
	var wg sync.WaitGroup
	var st stats.IngestionStats

	constraints, err := telemetry.DeriveConstraints(cfg.MQTTTopicFilter)
	if err != nil {
		return err
	}
	for workerID := 1; workerID <= cfg.WorkerCount; workerID++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			runProducerWorker(workerID, cfg, kafkaClient, queue.queue, &st, constraints)
		}(workerID)
	}

	subscribed := make(chan error, 1)
	mqttClient := mqtt.NewClient(mqttClientOptions(cfg, queue, &st, subscribed))

	// finish tears resources down in dependency order: stop new MQTT
	// deliveries, close the queue, wait for workers to drain it, then close
	// Kafka. Only called once all producer workers are confirmed stopped, so
	// it's always safe even if the caller gave up waiting on a shutdown timeout.
	finish := func() {
		mqttClient.Disconnect(250)
		queue.closeOnce()
		wg.Wait()
		kafkaClient.Close()
	}

	// Wait on the connect token's own Done channel alongside ctx so a SIGTERM
	// during a stalled connect attempt is honored immediately, instead of
	// token.Wait() blocking here with no bound but SetConnectTimeout.
	if err := waitForMQTTConnect(ctx, mqttClient.Connect(), cfg.MQTTBroker); err != nil {
		finish()
		return err
	}

	select {
	case err := <-subscribed:
		if err != nil {
			finish()
			return fmt.Errorf("subscribe mqtt topic %s: %w", cfg.MQTTTopicFilter, err)
		}
	case <-ctx.Done():
		finish()
		return ctx.Err()
	}

	log.Printf("ingestion bridge running: mqtt=%s filter=%s kafka=%s topic=%s workers=%d", cfg.MQTTBroker, cfg.MQTTTopicFilter, strings.Join(cfg.KafkaBrokers, ","), cfg.KafkaTopic, cfg.WorkerCount)
	<-ctx.Done()

	// Stop accepting new MQTT deliveries first, then drain the queued messages to Kafka.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if token := mqttClient.Unsubscribe(cfg.MQTTTopicFilter); !token.WaitTimeout(5 * time.Second) {
			log.Printf("mqtt unsubscribe timed out after 5s: filter=%s", cfg.MQTTTopicFilter)
		} else if err := token.Error(); err != nil {
			log.Printf("mqtt unsubscribe failed: %v", err)
		}
		finish()
	}()

	select {
	case <-shutdownCtx.Done():
		return fmt.Errorf("shutdown timeout: %w", shutdownCtx.Err())
	case <-done:
		log.Printf("ingestion bridge stopped: received=%d produced=%d dropped=%d failed=%d", st.Received(), st.Produced(), st.Dropped(), st.Failed())
		return ctx.Err()
	}
}

func waitForMQTTConnect(ctx context.Context, token mqtt.Token, broker string) error {
	select {
	case <-token.Done():
		if err := token.Error(); err != nil {
			return fmt.Errorf("connect mqtt broker %s: %w", broker, err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func mqttClientOptions(cfg config.Config, queue *queueGuard, st *stats.IngestionStats, subscribed chan<- error) *mqtt.ClientOptions {
	var subscribeOnce sync.Once

	return mqtt.NewClientOptions().
		AddBroker(cfg.MQTTBroker).
		SetClientID(cfg.MQTTClientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectTimeout(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			log.Printf("mqtt connection lost: %v", err)
		}).
		SetOnConnectHandler(func(client mqtt.Client) {
			// CleanSession discards the broker-side subscription on every
			// disconnect, and Paho does not resubscribe automatically, so we
			// redo it on every (re)connect or telemetry silently stops
			// flowing after the first network blip. Run it off this callback
			// goroutine with a bounded wait: Paho dispatches callbacks
			// serially, so blocking here would stall message delivery too.
			go resubscribe(client, cfg, subscribed, &subscribeOnce)
		}).
		SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
			e := envelope{
				Topic:   msg.Topic(),
				Payload: append([]byte(nil), msg.Payload()...),
				Metadata: telemetry.MessageMetadata{
					Duplicate: msg.Duplicate(),
					QOS:       msg.Qos(),
					Retained:  msg.Retained(),
					MessageID: msg.MessageID(),
				},
			}
			st.AddReceived()
			// Dropping under sustained backpressure is explicit for now; the raw hardware
			// stream is high-volume, and blocking here would stall MQTT client delivery.
			if !queue.enqueue(e) {
				st.AddDropped()
				log.Printf("ingestion queue full; dropped mqtt message topic=%s", msg.Topic())
			}
		})
}

// resubscribe issues the subscribe call for a (re)connect and reports the
// first attempt's outcome on subscribed; later reconnects only log, since
// nothing reads the channel a second time.
func resubscribe(client mqtt.Client, cfg config.Config, subscribed chan<- error, once *sync.Once) {
	token := client.Subscribe(cfg.MQTTTopicFilter, cfg.MQTTQOS, nil)
	if !token.WaitTimeout(5 * time.Second) {
		err := fmt.Errorf("subscribe timed out after 5s: filter=%s", cfg.MQTTTopicFilter)
		log.Printf("mqtt subscribe failed: %v", err)
		once.Do(func() { subscribed <- err })
		return
	}
	if err := token.Error(); err != nil {
		log.Printf("mqtt subscribe failed: filter=%s err=%v", cfg.MQTTTopicFilter, err)
		once.Do(func() { subscribed <- err })
		return
	}
	log.Printf("mqtt subscribed: filter=%s", cfg.MQTTTopicFilter)
	once.Do(func() { subscribed <- nil })
}

func runProducerWorker(workerID int, cfg config.Config, kafkaClient *kgo.Client, queue <-chan envelope, st *stats.IngestionStats, constraints telemetry.TopicConstraints) {
	for e := range queue {
		record, err := telemetry.BuildRecord(cfg.KafkaTopic, e.Topic, e.Payload, e.Metadata, constraints)
		if err != nil {
			st.AddFailed()
			log.Printf("worker=%d invalid mqtt message topic=%s err=%v", workerID, e.Topic, err)
			continue
		}

		// Use a fresh background timeout so shutdown can drain already accepted messages.
		produceCtx, cancel := context.WithTimeout(context.Background(), cfg.ProduceTimeout)
		err = kafkaClient.ProduceSync(produceCtx, record).FirstErr()
		cancel()
		if err != nil {
			st.AddFailed()
			log.Printf("worker=%d kafka produce failed topic=%s key=%s err=%v", workerID, record.Topic, string(record.Key), err)
			continue
		}

		produced := st.AddProduced()
		if cfg.LogEveryNRecords > 0 && produced%cfg.LogEveryNRecords == 0 {
			log.Printf("worker=%d produced=%d kafka_topic=%s", workerID, produced, cfg.KafkaTopic)
		}
	}
}
