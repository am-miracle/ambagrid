package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MetricPayload intentionally mirrors proto/telemetry.proto while staying JSON-only for local inspection.
type MetricPayload struct {
	DeviceID            string             `json:"device_id"`
	DeviceType          string             `json:"device_type"`
	TimestampUtc        int64              `json:"timestamp_utc"`
	SiteID              string             `json:"site_id"`
	HouseholdID         string             `json:"household_id,omitempty"`
	Metrics             *ElectricalMetrics `json:"metrics,omitempty"`
	InternalTemperature float32            `json:"internal_temperature"`
	RelayClosed         bool               `json:"relay_closed"`
	BatterySocPct       float32            `json:"battery_soc_pct"`
	SolarIrradiance     float32            `json:"solar_irradiance"`
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

type deviceKind string

const (
	deviceKindSmartMeter    deviceKind = "smartmeter"
	deviceKindBatteryBms    deviceKind = "battery_bms"
	deviceKindSolarInverter deviceKind = "solar_inverter"
)

var deviceTypeByKind = map[deviceKind]string{
	deviceKindSmartMeter:    "DEVICE_TYPE_SMART_METER",
	deviceKindBatteryBms:    "DEVICE_TYPE_BATTERY_BMS",
	deviceKindSolarInverter: "DEVICE_TYPE_SOLAR_INVERTER",
}

type deviceState struct {
	topic       string
	siteID      string
	deviceID    string
	deviceKind  deviceKind
	deviceType  string
	householdID string
	baseKwh     float64
	rng         *rand.Rand
}

// Passing this prefix swaps the generated site IDs for demoSiteIDs, matching
// the frontend mock fleet in apps/src/mocks/generate/fleet.ts.
const demoSitePrefix = "ng-demo"

var demoSiteIDs = []string{
	"rivers-bolo",
	"lagos-epe",
	"imo-ohaji",
	"nasarawa-duduguru",
	"bayelsa-oweikorogha",
	"akwa-ibom-ibeno",
	"bauchi-darazo",
	"benue-otukpo",
	"borno-monguno",
	"cross-river-ikom",
	"delta-burutu",
	"ebonyi-abakaliki",
	"enugu-nsukka",
	"kaduna-kachia",
	"kano-bagwai",
	"kwara-patigi",
	"niger-wushishi",
	"sokoto-illela",
	"taraba-ibi",
	"yobe-geidam",
}

func siteIDFor(sitePrefix string, siteNumber int) string {
	if sitePrefix == demoSitePrefix {
		return demoSiteIDs[siteNumber-1]
	}
	return fmt.Sprintf("%s-%02d", sitePrefix, siteNumber)
}

func newDeviceState(region, sitePrefix string, siteNumber int, kind deviceKind, number int) (*deviceState, error) {
	deviceType, ok := deviceTypeByKind[kind]
	if !ok {
		return nil, fmt.Errorf("unknown simulator device kind %q", kind)
	}
	siteID := siteIDFor(sitePrefix, siteNumber)
	site := fmt.Sprintf("%02d", siteNumber)
	device := fmt.Sprintf("%02d", number)

	state := &deviceState{
		siteID:     siteID,
		deviceKind: kind,
		deviceType: deviceType,
		rng:        rand.New(rand.NewSource(int64(siteNumber)*1000000 + int64(number)*1000 + time.Now().UnixNano()/1000)),
	}
	switch kind {
	case deviceKindSmartMeter:
		state.deviceID = fmt.Sprintf("met-%s%s", site, device)
		state.householdID = fmt.Sprintf("house-%s%s", site, device)
		state.baseKwh = 1420.50 + float64(siteNumber*100) + float64(number)
	case deviceKindBatteryBms:
		state.deviceID = fmt.Sprintf("bms-%s01", site)
	case deviceKindSolarInverter:
		state.deviceID = fmt.Sprintf("inv-%s01", site)
	}
	state.topic = fmt.Sprintf("%s/%s/%s/%s/telemetry", region, siteID, kind, state.deviceID)
	return state, nil
}

// publishConfig is simulator-wide MQTT/publish behavior, shared by every meter.
type publishConfig struct {
	qos          byte
	interval     time.Duration
	logPublishes bool
	publishCount *atomic.Int64
}

func main() {
	siteCountDefault, err := envInt("SIMULATOR_SITES", 5)
	if err != nil {
		log.Fatal(err)
	}
	metersPerSiteDefault, err := envInt("SIMULATOR_METERS_PER_SITE", 10)
	if err != nil {
		log.Fatal(err)
	}
	includeSiteKitDefault, err := envBool("SIMULATOR_INCLUDE_SITE_KIT", false)
	if err != nil {
		log.Fatal(err)
	}
	intervalDefault, err := envDuration("SIMULATOR_INTERVAL", 5*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	durationDefault, err := envDuration("SIMULATOR_DURATION", 0)
	if err != nil {
		log.Fatal(err)
	}
	qosDefault, err := envInt("MQTT_QOS", 0)
	if err != nil {
		log.Fatal(err)
	}
	logPublishesDefault, err := envBool("SIMULATOR_LOG_PUBLISHES", false)
	if err != nil {
		log.Fatal(err)
	}

	var (
		brokerURL      = flag.String("broker", envString("MQTT_BROKER", "tcp://localhost:1883"), "MQTT broker URL")
		username       = flag.String("username", envString("MQTT_USERNAME", ""), "MQTT username; can also be set with MQTT_USERNAME")
		password       = flag.String("password", "", "MQTT password; prefer MQTT_PASSWORD, which stays out of the process list")
		caFile         = flag.String("ca-file", envString("MQTT_CA_FILE", ""), "optional PEM CA bundle for TLS MQTT; can also be set with MQTT_CA_FILE")
		region         = flag.String("region", envString("MQTT_REGION", "africa-west"), "MQTT topic region prefix")
		sitePrefix     = flag.String("site-prefix", envString("MQTT_SITE_PREFIX", "ng-kaji"), "site ID prefix, or "+demoSitePrefix+" for named demo sites")
		siteCount      = flag.Int("sites", siteCountDefault, "number of simulated sites")
		metersPerSite  = flag.Int("meters-per-site", metersPerSiteDefault, "number of simulated meters per site")
		includeSiteKit = flag.Bool("include-site-kit", includeSiteKitDefault, "publish one battery BMS and solar inverter per site")
		interval       = flag.Duration("interval", intervalDefault, "publish interval")
		duration       = flag.Duration("duration", durationDefault, "optional total run duration; 0 runs until interrupted")
		qos            = flag.Uint("qos", uint(qosDefault), "MQTT QoS level")
		logPublishes   = flag.Bool("log-publishes", logPublishesDefault, "log every successful MQTT publish")
	)
	flag.Parse()
	if *password == "" {
		*password = os.Getenv("MQTT_PASSWORD")
	}

	if *siteCount < 1 || *metersPerSite < 1 {
		log.Fatal("sites and meters-per-site must both be greater than zero")
	}
	// Rejected rather than topped up with generated IDs, which would publish
	// half named and half numbered sites under one run and read as two fleets.
	if *sitePrefix == demoSitePrefix && *siteCount > len(demoSiteIDs) {
		log.Fatalf("site-prefix %s has only %d named sites; -sites %d is too many", demoSitePrefix, len(demoSiteIDs), *siteCount)
	}
	if *qos > 2 {
		log.Fatal("qos must be 0, 1, or 2")
	}
	if *interval <= 0 {
		log.Fatal("interval must be greater than zero")
	}
	if (*username == "") != (*password == "") {
		log.Fatal("username and password must be set together")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	tlsCfg, err := tlsConfig(*caFile)
	if err != nil {
		log.Fatal(err)
	}

	opts := mqtt.NewClientOptions().
		AddBroker(*brokerURL).
		SetClientID(fmt.Sprintf("ambagrid_virtual_meter_%d", time.Now().UnixNano())).
		SetUsername(*username).
		SetPassword(*password).
		SetTLSConfig(tlsCfg).
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
	totalDevices := totalMeters
	if *includeSiteKit {
		totalDevices += *siteCount * 2
	}
	var publishCount atomic.Int64
	log.Printf("virtual meter simulator online: broker=%s sites=%d meters=%d devices=%d site_kit=%t interval=%s duration=%s", *brokerURL, *siteCount, totalMeters, totalDevices, *includeSiteKit, *interval, *duration)

	cfg := publishConfig{
		qos:          byte(*qos),
		interval:     *interval,
		logPublishes: *logPublishes,
		publishCount: &publishCount,
	}

	kinds := []deviceKind{deviceKindBatteryBms, deviceKindSolarInverter}
	var wg sync.WaitGroup
	start := func(siteNumber int, kind deviceKind, number int) {
		state, err := newDeviceState(*region, *sitePrefix, siteNumber, kind, number)
		if err != nil {
			log.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			runDevice(ctx, client, state, cfg)
		}()
	}
	for siteNumber := 1; siteNumber <= *siteCount; siteNumber++ {
		for meterNumber := 1; meterNumber <= *metersPerSite; meterNumber++ {
			start(siteNumber, deviceKindSmartMeter, meterNumber)
		}
		if *includeSiteKit {
			for _, kind := range kinds {
				start(siteNumber, kind, 1)
			}
		}
	}

	<-ctx.Done()
	log.Println("shutdown requested; waiting for simulated meters to stop")
	wg.Wait()
	log.Printf("virtual meter simulator stopped: published=%d", publishCount.Load())
}

func envString(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func envBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return parsed, nil
}

func tlsConfig(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read MQTT CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("MQTT CA file %s contains no PEM certificates", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

func runDevice(ctx context.Context, client mqtt.Client, state *deviceState, cfg publishConfig) {
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	// Publish once immediately so short smoke tests do not wait a full interval.
	publishDeviceReading(client, state, cfg)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publishDeviceReading(client, state, cfg)
		}
	}
}

func publishDeviceReading(client mqtt.Client, state *deviceState, cfg publishConfig) {
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

func nextPayload(state *deviceState, interval time.Duration) MetricPayload {
	payload := MetricPayload{
		DeviceID:     state.deviceID,
		DeviceType:   state.deviceType,
		TimestampUtc: time.Now().Unix(),
		SiteID:       state.siteID,
		HouseholdID:  state.householdID,
	}

	switch state.deviceKind {
	case deviceKindBatteryBms:
		payload.InternalTemperature = randomFloat(state.rng, 27.0, 38.0)
		payload.BatterySocPct = randomFloat(state.rng, 45.0, 98.0)
	case deviceKindSolarInverter:
		payload.InternalTemperature = randomFloat(state.rng, 32.0, 62.0)
		payload.SolarIrradiance = solarIrradiance(time.Now().UTC().Hour())
	default:
		voltage := randomFloat(state.rng, 215.0, 240.0)
		current := randomFloat(state.rng, 0.5, 18.0)
		activePower := (voltage * current) / 1000.0
		state.baseKwh += float64(activePower) * interval.Hours()
		payload.Metrics = &ElectricalMetrics{
			Voltage:     voltage,
			Current:     current,
			ActivePower: activePower,
			Frequency:   randomFloat(state.rng, 49.8, 50.2),
			TotalKwh:    state.baseKwh,
		}
		payload.InternalTemperature = randomFloat(state.rng, 28.0, 42.0)
		payload.RelayClosed = true
	}
	return payload
}

func solarIrradiance(hour int) float32 {
	if hour <= 6 || hour >= 18 {
		return 0
	}
	// Approximate a clear-day irradiance curve without pulling in weather data.
	return float32(math.Sin(float64(hour-6)/12.0*math.Pi) * 850.0)
}
