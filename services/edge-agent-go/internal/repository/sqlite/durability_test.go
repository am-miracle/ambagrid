package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenEnablesFullFsync(t *testing.T) {
	t.Parallel()

	store, err := Open(context.Background(), Config{
		Path:                   filepath.Join(t.TempDir(), "queue.db"),
		SiteID:                 "site-01",
		GatewayID:              "gateway-01",
		MaxStorageBytes:        8 << 20,
		WarningPercent:         70,
		CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	defer store.Close()

	var enabled int
	if err := store.db.QueryRowContext(context.Background(), "PRAGMA fullfsync").Scan(&enabled); err != nil {
		t.Fatalf("read fullfsync setting: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("fullfsync = %d, want 1", enabled)
	}
}
