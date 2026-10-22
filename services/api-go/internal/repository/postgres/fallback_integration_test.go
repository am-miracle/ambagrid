package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"api-go/internal/domain"
	"api-go/internal/repository"
)

func TestFallbackTxLifecycleInPostgres(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires DATABASE_URL and engine migrations")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	siteID := "it-site-" + suffix
	assetID := "it-asset-" + suffix
	eventKey := domain.NewEventKey(siteID, 1)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed database: %v", err)
		}
	}
	mustExec("INSERT INTO sites (site_id, name) VALUES ($1, 'Fallback integration test')", siteID)
	mustExec("INSERT INTO assets (asset_id, site_id, asset_type, last_seen_at) VALUES ($1, $2, 'battery_bms', now())", assetID, siteID)

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM sms_fallback_receipts WHERE event_key = $1", eventKey)
		_, _ = pool.Exec(ctx, "DELETE FROM sms_fallback_events WHERE event_key = $1", eventKey)
		_, _ = pool.Exec(ctx, "DELETE FROM command_event_outbox WHERE aggregate_id IN (SELECT alert_id::text FROM alerts WHERE site_id = $1)", siteID)
		_, _ = pool.Exec(ctx, "DELETE FROM alerts WHERE site_id = $1", siteID)
		_, _ = pool.Exec(ctx, "DELETE FROM assets WHERE asset_id = $1", assetID)
		_, _ = pool.Exec(ctx, "DELETE FROM sites WHERE site_id = $1", siteID)
	})

	store := NewFallbackStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("advisory lock and commit", func(t *testing.T) {
		committed, err := store.RunFallbackTx(ctx, eventKey, func(tx repository.FallbackTx) (bool, error) {
			stored, err := tx.FindFallbackEvent(ctx, eventKey)
			if err != nil {
				return false, err
			}
			if stored != nil {
				t.Fatal("event should not exist yet")
			}

			assetType, err := tx.LookupAssetType(ctx, assetID, siteID)
			if err != nil {
				return false, err
			}
			if assetType != "battery_bms" {
				t.Fatalf("asset type = %q, want battery_bms", assetType)
			}

			alertID, err := tx.CreateAlert(ctx, domain.CreateAlertCommand{
				AssetID: assetID, SiteID: siteID,
				Kind: "battery_overheat", Reason: "temperature exceeded threshold",
				OccurredAt: now, SourceEventID: eventKey,
			})
			if err != nil {
				return false, err
			}
			if alertID == "" {
				t.Fatal("CreateAlert returned empty alert ID")
			}

			if err := tx.PublishOutboxEvent(ctx, "alerts", "alert.opened", "alert", alertID, []byte(`{"test":true}`)); err != nil {
				return false, err
			}

			if err := tx.InsertFallbackEvent(ctx, domain.InsertFallbackEventCommand{
				EventKey: eventKey, SiteID: siteID, Sequence: 1,
				AssetID: assetID, Code: domain.BatteryOverheatCode,
				EventAt: now, AlertID: alertID,
			}); err != nil {
				return false, err
			}

			return true, nil
		})
		if err != nil {
			t.Fatalf("RunFallbackTx: %v", err)
		}
		if !committed {
			t.Fatal("expected created=true")
		}
	})

	t.Run("find persisted event", func(t *testing.T) {
		_, err := store.RunFallbackTx(ctx, eventKey, func(tx repository.FallbackTx) (bool, error) {
			stored, err := tx.FindFallbackEvent(ctx, eventKey)
			if err != nil {
				return false, err
			}
			if stored == nil {
				t.Fatal("expected persisted event")
			}
			if stored.SiteID != siteID || stored.AssetID != assetID || stored.Code != domain.BatteryOverheatCode || stored.Sequence != 1 {
				t.Fatalf("stored event = %+v", stored)
			}
			return false, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("find alert by source event", func(t *testing.T) {
		_, err := store.RunFallbackTx(ctx, eventKey, func(tx repository.FallbackTx) (bool, error) {
			alertID, err := tx.FindAlertBySourceEvent(ctx, eventKey)
			if err != nil {
				return false, err
			}
			if alertID == "" {
				t.Fatal("expected alert for source event")
			}
			return false, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("find open alert", func(t *testing.T) {
		_, err := store.RunFallbackTx(ctx, eventKey, func(tx repository.FallbackTx) (bool, error) {
			alertID, err := tx.FindOpenAlert(ctx, siteID, assetID, "battery_overheat")
			if err != nil {
				return false, err
			}
			if alertID == "" {
				t.Fatal("expected open alert")
			}
			return false, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("idempotent receipt insertion", func(t *testing.T) {
		receipt := domain.SMSReceipt{
			ProviderMessageID: "msg-" + suffix,
			Sender:            "+234800000",
			Recipient:         "+234900000",
			ReceivedAt:        now,
			GatewayID:         "gw-01",
		}
		pricing := domain.SMSPricing{
			OutboundCostMinor: 350,
			InboundCostMinor:  50,
			Currency:          "NGN",
		}

		var firstInserted, secondInserted bool
		_, err := store.RunFallbackTx(ctx, eventKey, func(tx repository.FallbackTx) (bool, error) {
			var err error
			firstInserted, err = tx.InsertReceipt(ctx, eventKey, receipt, pricing)
			return false, err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !firstInserted {
			t.Fatal("first insert should succeed")
		}

		_, err = store.RunFallbackTx(ctx, eventKey, func(tx repository.FallbackTx) (bool, error) {
			var err error
			secondInserted, err = tx.InsertReceipt(ctx, eventKey, receipt, pricing)
			return false, err
		})
		if err != nil {
			t.Fatal(err)
		}
		if secondInserted {
			t.Fatal("duplicate insert should be idempotent (return false)")
		}
	})

	t.Run("receipt event key lookup", func(t *testing.T) {
		_, err := store.RunFallbackTx(ctx, eventKey, func(tx repository.FallbackTx) (bool, error) {
			key, err := tx.FindReceiptEventKey(ctx, "msg-"+suffix)
			if err != nil {
				return false, err
			}
			if key != eventKey {
				t.Fatalf("receipt event key = %q, want %q", key, eventKey)
			}
			return false, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("monthly SMS usage", func(t *testing.T) {
		rentalMinor := int64(1000)
		_, err := store.RunFallbackTx(ctx, eventKey, func(tx repository.FallbackTx) (bool, error) {
			usage, err := tx.GetMonthlySMSUsage(ctx, now, rentalMinor)
			if err != nil {
				return false, err
			}
			// 350 outbound + 50 inbound + 1000 rental = 1400
			if usage < 1400 {
				t.Fatalf("monthly usage = %d, want >= 1400 (outbound + inbound + rental)", usage)
			}
			return false, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunFallbackTxRollsBackOnCallbackError(t *testing.T) {
	tx := &fakeTransaction{
		rows: []pgx.Row{fakeRow(func(...any) error { return nil })},
	}
	pool := &fakeDatabasePool{tx: tx}
	store := NewFallbackStore(pool)

	_, err := store.RunFallbackTx(context.Background(), "edge:site:1", func(repository.FallbackTx) (bool, error) {
		return false, context.DeadlineExceeded
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if tx.committed {
		t.Fatal("transaction should not be committed on callback error")
	}
}
