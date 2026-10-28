// fleet-simulator supplies local demo sites through the real collector and uploader.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"edge-agent-go/internal/adapter/simulator"
	"edge-agent-go/internal/collector"
	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/repository/sqlite"
	"edge-agent-go/internal/uploader"
)

func main() {
	if err := run(); err != nil {
		slog.Error("local fleet stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	sitesFile := flag.String("sites-file", "/etc/ambagrid/sites.json", "demo site metadata")
	queueDir := flag.String("queue-dir", "/var/lib/ambagrid-edge/fleet", "per-site SQLite queues")
	endpoint := flag.String("endpoint", "http://ingestion-go:8090/v1/ingest", "local HTTP ingestion endpoint")
	skip := flag.String("skip-site", "lagos-epe", "site served by the standalone outage-test edge")
	meters := flag.Int("meters-per-site", 10, "smart meters per site")
	flag.Parse()
	if *meters < 1 || *meters > 100 {
		return fmt.Errorf("meters-per-site must be between 1 and 100")
	}
	raw, err := os.ReadFile(*sitesFile)
	if err != nil {
		return err
	}
	var sites []struct {
		ID     string `json:"site_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &sites); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var workers sync.WaitGroup
	var stores []*sqlite.Store
	defer func() {
		cancel()
		workers.Wait()
		for _, store := range stores {
			if err := store.Close(); err != nil {
				slog.Error("close demo queue", "error", err)
			}
		}
	}()
	for _, site := range sites {
		if site.Status != "active" || site.ID == *skip {
			continue
		}
		if site.ID == "" || filepath.Base(site.ID) != site.ID || site.ID == "." || site.ID == ".." {
			return fmt.Errorf("invalid demo site ID %q", site.ID)
		}
		gatewayID := "local-fleet-" + site.ID
		store, err := sqlite.Open(ctx, sqlite.Config{
			Path: filepath.Join(*queueDir, site.ID+".db"), SiteID: site.ID, GatewayID: gatewayID,
			MaxStorageBytes: 32 << 20, WarningPercent: 70, CriticalReservePercent: 10,
		})
		if err != nil {
			return fmt.Errorf("open %s queue: %w", site.ID, err)
		}
		stores = append(stores, store)
		source := &fleetSource{hardware: simulator.New(site.ID), siteID: site.ID, meters: *meters}
		reader, err := collector.New(collector.Config{
			SiteID: site.ID, Region: "africa-west", Interval: 5 * time.Second, BatteryOverheatC: 55,
		}, store, source)
		if err != nil {
			return err
		}
		client := uploader.NewIngestClient(*endpoint, "local-fleet-"+site.ID, 10*time.Second)
		writer := uploader.New(uploader.Config{GatewayID: gatewayID, PollInterval: 2 * time.Second}, store, client, store)
		workers.Add(2)
		go func() { defer workers.Done(); reader.Run(ctx) }()
		go func() { defer workers.Done(); writer.Run(ctx) }()
		slog.Info("demo site started", "site_id", site.ID, "meters", *meters)
	}
	if len(stores) == 0 {
		return fmt.Errorf("no active demo sites selected")
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, store := range stores {
					if _, err := store.Purge(ctx, time.Hour); err != nil && ctx.Err() == nil {
						slog.Error("purge demo queue", "error", err)
					}
				}
			}
		}
	}()
	<-ctx.Done()
	return nil
}

type fleetSource struct {
	hardware *simulator.Adapter
	siteID   string
	meters   int
}

func (s *fleetSource) Read(ctx context.Context) ([]domain.Reading, error) {
	readings, err := s.hardware.Read(ctx)
	if err != nil {
		return nil, err
	}
	var result []domain.Reading
	for _, reading := range readings {
		count := 1
		if reading.AssetType == domain.AssetSmartMeter {
			count = s.meters
		}
		for i := 1; i <= count; i++ {
			r := reading
			r.DeviceID = fmt.Sprintf("%s-%s-%02d", s.siteID, r.AssetType, i)
			if r.Metrics != nil {
				metrics := *r.Metrics
				factor := 0.5 + float64(i)/10
				if metrics.Current != nil {
					metrics.Current = new(*metrics.Current * factor)
				}
				if metrics.ActivePower != nil {
					metrics.ActivePower = new(*metrics.ActivePower * factor)
				}
				if metrics.TotalKWh != nil {
					metrics.TotalKWh = new(*metrics.TotalKWh + float64(i)*17)
				}
				r.Metrics = &metrics
			}
			var payload map[string]any
			if err := json.Unmarshal(r.Payload, &payload); err != nil {
				return nil, err
			}
			payload["device_id"] = r.DeviceID
			if r.Metrics != nil {
				payload["metrics"] = r.Metrics
			}
			if r.Payload, err = json.Marshal(payload); err != nil {
				return nil, err
			}
			result = append(result, r)
		}
	}
	return result, nil
}
