package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MetricPayload intentionally mirrors proto/telemetry.proto while staying JSON-only for local inspection.
type MetricPayload struct {
	DeviceID            string            `json:"device_id"`
	DeviceType          string            `json:"device_type"`
	TimestampUtc        int64             `json:"timestamp_utc"`
	SiteID              string            `json:"site_id"`
	HouseholdID         string            `json:"household_id,omitempty"`
	Metrics             ElectricalMetrics `json:"metrics"`
	InternalTemperature float32           `json:"internal_temperature"`
	RelayClosed         bool              `json:"relay_closed"`
	BatterySocPct       float32           `json:"battery_soc_pct"`
	SolarIrradiance     float32           `json:"solar_irradiance"`
}

type ElectricalMetrics struct {
	Voltage     float32 `json:"voltage"`
	Current     float32 `json:"current"`
	ActivePower float32 `json:"active_power"`
	Frequency   float32 `json:"frequency"`
	TotalKwh    float64 `json:"total_kwh"`
}

func randomFloat(rng *rand.Rand, min, max float64) float32 {
	return float32(rng.Float64()*(max-min) + min)
}

// meterState is the identity and mutable readings state for one simulated meter.
type meterState struct {
	topic       string
	siteID      string
	deviceID    string
	householdID string
	baseKwh     float64
	rng         *rand.Rand
}

func newMeterState(region, sitePrefix string, siteNumber, meterNumber int) *meterState {
	siteID := fmt.Sprintf("%s-%02d", sitePrefix, siteNumber)
	deviceID := fmt.Sprintf("met-%02d%02d", siteNumber, meterNumber)
	return &meterState{
		topic:       fmt.Sprintf("%s/%s/smartmeter/%s/telemetry", region, siteID, deviceID),
		siteID:      siteID,
		deviceID:    deviceID,
		householdID: fmt.Sprintf("house-%02d%02d", siteNumber, meterNumber),
		baseKwh:     1420.50 + float64(siteNumber*100) + float64(meterNumber),
		rng:         rand.New(rand.NewSource(int64(siteNumber)*1000000 + int64(meterNumber)*1000 + time.Now().UnixNano()/1000)),
	}
}

// publishConfig is simulator-wide MQTT/publish behavior, shared by every meter.
type publishConfig struct {
	qos          byte
	interval     time.Duration
	logPublishes bool
	publishCount *atomic.Int64
}

func main() {
	var (
		brokerURL     = flag.String("broker", "tcp://localhost:1883", "MQTT broker URL")
		region        = flag.String("region", "africa-west", "MQTT topic region prefix")
		sitePrefix    = flag.String("site-prefix", "ng-kaji", "site ID prefix")
		siteCount     = flag.Int("sites", 5, "number of simulated sites")
		metersPerSite = flag.Int("meters-per-site", 10, "number of simulated meters per site")
		interval      = flag.Duration("interval", 5*time.Second, "publish interval")
		duration      = flag.Duration("duration", 0, "optional total run duration; 0 runs until interrupted")
		qos           = flag.Uint("qos", 0, "MQTT QoS level")
		logPublishes  = flag.Bool("log-publishes", false, "log every successful MQTT publish")
	)
	flag.Parse()

	if *siteCount < 1 || *metersPerSite < 1 {
		log.Fatal("sites and meters-per-site must both be greater than zero")
	}
	if *qos > 2 {
		log.Fatal("qos must be 0, 1, or 2")
	}
	if *interval <= 0 {
		log.Fatal("interval must be greater than zero")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	opts := mqtt.NewClientOptions().
		AddBroker(*brokerURL).
		SetClientID(fmt.Sprintf("ambagrid_virtual_meter_%d", time.Now().UnixNano())).
		SetKeepAlive(60 * time.Second).
		SetConnectTimeout(5 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetAutoReconnect(true)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("failed to connect to MQTT broker %s: %v", *brokerURL, token.Error())
	}
	defer client.Disconnect(250)

	totalMeters := *siteCount * *metersPerSite
	var publishCount atomic.Int64
	log.Printf("virtual meter simulator online: broker=%s sites=%d meters=%d interval=%s duration=%s", *brokerURL, *siteCount, totalMeters, *interval, *duration)

	cfg := publishConfig{
		qos:          byte(*qos),
		interval:     *interval,
		logPublishes: *logPublishes,
		publishCount: &publishCount,
	}

	var wg sync.WaitGroup
	for siteNumber := 1; siteNumber <= *siteCount; siteNumber++ {
		for meterNumber := 1; meterNumber <= *metersPerSite; meterNumber++ {
			wg.Add(1)
			go func(siteNumber, meterNumber int) {
				defer wg.Done()
				runMeter(ctx, client, *region, *sitePrefix, siteNumber, meterNumber, cfg)
			}(siteNumber, meterNumber)
		}
	}

	<-ctx.Done()
	log.Println("shutdown requested; waiting for simulated meters to stop")
	wg.Wait()
	log.Printf("virtual meter simulator stopped: published=%d", publishCount.Load())
}

func runMeter(ctx context.Context, client mqtt.Client, region, sitePrefix string, siteNumber, meterNumber int, cfg publishConfig) {
	state := newMeterState(region, sitePrefix, siteNumber, meterNumber)

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	// Publish once immediately so short smoke tests do not wait a full interval.
	publishMeterReading(client, state, cfg)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publishMeterReading(client, state, cfg)
		}
	}
}

func publishMeterReading(client mqtt.Client, state *meterState, cfg publishConfig) {
	payload := nextPayload(state, cfg.interval)
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("failed to marshal payload for %s: %v", state.deviceID, err)
		return
	}

	token := client.Publish(state.topic, cfg.qos, false, jsonBytes)
	if !token.WaitTimeout(5 * time.Second) {
		log.Printf("publish timeout: topic=%s", state.topic)
		return
	}
	if err := token.Error(); err != nil {
		log.Printf("publish failed: topic=%s err=%v", state.topic, err)
		return
	}

	count := cfg.publishCount.Add(1)
	if cfg.logPublishes {
		log.Printf("published telemetry: topic=%s count=%d", state.topic, count)
	}
}

func nextPayload(state *meterState, interval time.Duration) MetricPayload {
	voltage := randomFloat(state.rng, 215.0, 240.0)
	current := randomFloat(state.rng, 0.5, 18.0)
	activePower := (voltage * current) / 1000.0
	frequency := randomFloat(state.rng, 49.8, 50.2)
	state.baseKwh += float64(activePower) * interval.Hours()

	hour := time.Now().UTC().Hour()
	var solarIrradiance float32
	if hour > 6 && hour < 18 {
		// Approximate a clear-day irradiance curve without pulling in weather data.
		solarIrradiance = float32(math.Sin(float64(hour-6)/12.0*math.Pi) * 850.0)
	}

	return MetricPayload{
		DeviceID:     state.deviceID,
		DeviceType:   "DEVICE_TYPE_SMART_METER",
		TimestampUtc: time.Now().Unix(),
		SiteID:       state.siteID,
		HouseholdID:  state.householdID,
		Metrics: ElectricalMetrics{
			Voltage:     voltage,
			Current:     current,
			ActivePower: activePower,
			Frequency:   frequency,
			TotalKwh:    state.baseKwh,
		},
		InternalTemperature: randomFloat(state.rng, 28.0, 42.0),
		RelayClosed:         true,
		BatterySocPct:       randomFloat(state.rng, 45.0, 98.0),
		SolarIrradiance:     solarIrradiance,
	}
}
