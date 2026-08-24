package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/big"
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
	BatterySocPct       float32           `json:"battery_soc_pct,omitempty"`
	SolarIrradiance     float32           `json:"solar_irradiance,omitempty"`
}

type ElectricalMetrics struct {
	Voltage     float32 `json:"voltage"`
	Current     float32 `json:"current"`
	ActivePower float32 `json:"active_power"`
	Frequency   float32 `json:"frequency"`
	TotalKwh    float64 `json:"total_kwh"`
}

func randomFloat(min, max float64) float32 {
	// crypto/rand avoids correlated meter behavior when many goroutines start together.
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return float32(min)
	}

	scale := float64(n.Int64()) / 10000.0
	return float32(min + scale*(max-min))
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

	var wg sync.WaitGroup
	for siteNumber := 1; siteNumber <= *siteCount; siteNumber++ {
		for meterNumber := 1; meterNumber <= *metersPerSite; meterNumber++ {
			wg.Add(1)
			go func(siteNumber, meterNumber int) {
				defer wg.Done()
				runMeter(ctx, client, *region, *sitePrefix, siteNumber, meterNumber, *interval, byte(*qos), &publishCount, *logPublishes)
			}(siteNumber, meterNumber)
		}
	}

	<-ctx.Done()
	log.Println("shutdown requested; waiting for simulated meters to stop")
	wg.Wait()
	log.Printf("virtual meter simulator stopped: published=%d", publishCount.Load())
}

func runMeter(ctx context.Context, client mqtt.Client, region, sitePrefix string, siteNumber, meterNumber int, interval time.Duration, qos byte, publishCount *atomic.Int64, logPublishes bool) {
	siteID := fmt.Sprintf("%s-%02d", sitePrefix, siteNumber)
	deviceID := fmt.Sprintf("met-%02d%02d", siteNumber, meterNumber)
	householdID := fmt.Sprintf("house-%02d%02d", siteNumber, meterNumber)
	topic := fmt.Sprintf("%s/%s/smartmeter/%s/telemetry", region, siteID, deviceID)
	baseKwh := 1420.50 + float64(siteNumber*100) + float64(meterNumber)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Publish once immediately so short smoke tests do not wait a full interval.
	publishMeterReading(client, topic, siteID, deviceID, householdID, &baseKwh, qos, publishCount, logPublishes)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publishMeterReading(client, topic, siteID, deviceID, householdID, &baseKwh, qos, publishCount, logPublishes)
		}
	}
}

func publishMeterReading(client mqtt.Client, topic, siteID, deviceID, householdID string, baseKwh *float64, qos byte, publishCount *atomic.Int64, logPublishes bool) {
	payload := nextPayload(siteID, deviceID, householdID, baseKwh)
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("failed to marshal payload for %s: %v", deviceID, err)
		return
	}

	token := client.Publish(topic, qos, false, jsonBytes)
	if !token.WaitTimeout(5 * time.Second) {
		log.Printf("publish timeout: topic=%s", topic)
		return
	}
	if err := token.Error(); err != nil {
		log.Printf("publish failed: topic=%s err=%v", topic, err)
		return
	}

	count := publishCount.Add(1)
	if logPublishes {
		log.Printf("published telemetry: topic=%s count=%d", topic, count)
	}
}

func nextPayload(siteID, deviceID, householdID string, baseKwh *float64) MetricPayload {
	voltage := randomFloat(215.0, 240.0)
	current := randomFloat(0.5, 18.0)
	activePower := (voltage * current) / 1000.0
	frequency := randomFloat(49.8, 50.2)
	*baseKwh += float64(activePower) / 360.0

	hour := time.Now().Hour()
	var solarIrradiance float32
	if hour > 6 && hour < 18 {
		// Approximate a clear-day irradiance curve without pulling in weather data.
		solarIrradiance = float32(math.Sin(float64(hour-6)/12.0*math.Pi) * 850.0)
	}

	return MetricPayload{
		DeviceID:     deviceID,
		DeviceType:   "DEVICE_TYPE_SMART_METER",
		TimestampUtc: time.Now().Unix(),
		SiteID:       siteID,
		HouseholdID:  householdID,
		Metrics: ElectricalMetrics{
			Voltage:     voltage,
			Current:     current,
			ActivePower: activePower,
			Frequency:   frequency,
			TotalKwh:    *baseKwh,
		},
		InternalTemperature: randomFloat(28.0, 42.0),
		RelayClosed:         true,
		BatterySocPct:       randomFloat(45.0, 98.0),
		SolarIrradiance:     solarIrradiance,
	}
}
