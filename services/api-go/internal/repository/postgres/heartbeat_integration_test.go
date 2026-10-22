package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-go/internal/domain"
	"api-go/internal/page"
	"api-go/internal/repository"
)

func TestHeartbeatTxLifecycleInPostgres(t *testing.T) {
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
	siteID := "it-hb-site-" + hex.EncodeToString(random[:])
	if _, err := pool.Exec(ctx, "INSERT INTO sites (site_id, name) VALUES ($1, 'Heartbeat integration test')", siteID); err != nil {
		t.Fatalf("seed database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM sms_heartbeat_receipts WHERE heartbeat_key IN (SELECT heartbeat_key FROM sms_heartbeats WHERE site_id = $1)", siteID)
		_, _ = pool.Exec(ctx, "DELETE FROM sms_heartbeats WHERE site_id = $1", siteID)
		_, _ = pool.Exec(ctx, "DELETE FROM sites WHERE site_id = $1", siteID)
	})

	sentAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	heartbeat := domain.GatewayHeartbeat{SiteID: siteID, GatewayID: "gateway-01", Sequence: 1, Interval: 30 * time.Minute, SentAt: sentAt}
	receipt := domain.SMSReceipt{
		ProviderMessageID: "it-hb-msg-" + siteID, Sender: "+2348000000000", Recipient: "1234",
		ReceivedAt: sentAt.Add(10 * time.Second), GatewayID: "gateway-01",
	}
	pricing := domain.SMSPricing{OutboundCostMinor: 400, InboundCostMinor: 100, Currency: "NGN"}
	store := NewHeartbeatStore(pool)

	insert := func() (bool, error) {
		return store.RunHeartbeatTx(ctx, heartbeat.Key(), func(tx repository.HeartbeatTx) (bool, error) {
			stored, err := tx.FindHeartbeat(ctx, heartbeat.Key())
			if err != nil || stored != nil {
				return false, err
			}
			if err := tx.InsertHeartbeat(ctx, heartbeat, heartbeat.ContactAt(receipt.ReceivedAt)); err != nil {
				return false, err
			}
			return tx.InsertHeartbeatReceipt(ctx, heartbeat.Key(), receipt, pricing)
		})
	}
	if inserted, err := insert(); err != nil || !inserted {
		t.Fatalf("first insert = %v, %v", inserted, err)
	}

	_, err = store.RunHeartbeatTx(ctx, heartbeat.Key(), func(tx repository.HeartbeatTx) (bool, error) {
		stored, err := tx.FindHeartbeat(ctx, heartbeat.Key())
		if err != nil {
			return false, err
		}
		if stored == nil || !stored.Matches(heartbeat) {
			t.Fatalf("stored heartbeat = %+v, want match", stored)
		}
		inserted, err := tx.InsertHeartbeatReceipt(ctx, heartbeat.Key(), receipt, pricing)
		if err != nil || inserted {
			t.Fatalf("duplicate receipt insert = %v, %v; want ignored", inserted, err)
		}
		key, err := tx.FindHeartbeatReceiptKey(ctx, receipt.ProviderMessageID)
		if err != nil || key != heartbeat.Key() {
			t.Fatalf("receipt key = %q, %v", key, err)
		}
		usage, err := tx.GetMonthlySMSUsage(ctx, receipt.ReceivedAt, 0)
		if err != nil || usage < 500 {
			t.Fatalf("monthly usage = %d, %v; want heartbeat receipt counted", usage, err)
		}
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sites, err := NewStore(pool, 5*time.Second).ListSites(ctx, domain.SiteQuery{
		Limit: 1, Cursor: page.Encode(siteID[:len(siteID)-1]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites.Items) != 1 || sites.Items[0].SiteID != siteID {
		t.Fatalf("sites = %+v, want seeded site", sites.Items)
	}
	site := sites.Items[0]
	if site.LastSMSHeartbeatAt == nil || !site.LastSMSHeartbeatAt.Equal(sentAt) {
		t.Fatalf("last SMS heartbeat = %v, want %v", site.LastSMSHeartbeatAt, sentAt)
	}
	if want := sentAt.Add(75 * time.Minute); site.SMSHeartbeatExpiresAt == nil || !site.SMSHeartbeatExpiresAt.Equal(want) {
		t.Fatalf("SMS heartbeat expiry = %v, want %v from the declared interval", site.SMSHeartbeatExpiresAt, want)
	}
	if site.LastContactAt != nil {
		t.Fatalf("SMS heartbeat must not populate HTTP last contact, got %v", site.LastContactAt)
	}
}
