package sqlite_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"edge-agent-go/internal/domain"
	sqlite "edge-agent-go/internal/repository/sqlite"
)

func TestCommittedEventSurvivesAbruptProcessDeath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestQueueWriterHelperProcess$")
	cmd.Env = append(os.Environ(), "AMBAGRID_QUEUE_HELPER_PATH="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("open helper stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start queue writer helper: %v", err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "READY\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("wait for committed event: line %q, error %v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill queue writer helper: %v", err)
	}
	_ = cmd.Wait()

	q, err := sqlite.Open(context.Background(), testConfig(path))
	if err != nil {
		t.Fatalf("reopen queue after process death: %v", err)
	}
	defer q.Close()
	records, err := q.Ready(context.Background(), 10, 1<<20)
	if err != nil {
		t.Fatalf("read queue after process death: %v", err)
	}
	if len(records) != 1 || string(records[0].Payload) != "committed-before-kill" {
		t.Fatalf("records after process death = %#v", records)
	}
}

func TestQueueWriterHelperProcess(t *testing.T) {
	path := os.Getenv("AMBAGRID_QUEUE_HELPER_PATH")
	if path == "" {
		return
	}
	q, err := sqlite.Open(context.Background(), testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(context.Background(), testEvent([]byte("committed-before-kill"), domain.PriorityNormal)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "READY"); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestCommittedEventsSurviveRestartWithoutReusingSequence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	cfg := sqlite.Config{
		Path:                   path,
		SiteID:                 "ng-kaji-01",
		GatewayID:              "gateway-01",
		MaxStorageBytes:        8 << 20,
		WarningPercent:         70,
		CriticalReservePercent: 10,
	}

	q, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}

	first, err := q.Enqueue(ctx, domain.Event{
		DeviceID:       "met-0101",
		AssetType:      domain.AssetSmartMeter,
		MQTTTopic:      "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry",
		Payload:        []byte(`{"voltage":231.4}`),
		EventTimestamp: time.Unix(1_745_500_000, 0),
		EdgeReceivedAt: time.Unix(1_745_500_001, 0),
	})
	if err != nil {
		t.Fatalf("enqueue first event: %v", err)
	}
	if first != 1 {
		t.Fatalf("first sequence = %d, want 1", first)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("close queue: %v", err)
	}

	q, err = sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer q.Close()

	records, err := q.Ready(ctx, 10, 1<<20)
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("ready records = %d, want 1", len(records))
	}
	if records[0].Sequence != first || records[0].SiteID != cfg.SiteID || records[0].GatewayID != cfg.GatewayID {
		t.Fatalf("reloaded identity = (%s, %s, %d), want (%s, %s, %d)", records[0].SiteID, records[0].GatewayID, records[0].Sequence, cfg.SiteID, cfg.GatewayID, first)
	}

	if err := q.MarkUploaded(ctx, first, time.Now().UTC()); err != nil {
		t.Fatalf("mark first event uploaded: %v", err)
	}
	if err := q.Ack(ctx, first); err != nil {
		t.Fatalf("ack first event: %v", err)
	}
	second, err := q.Enqueue(ctx, domain.Event{
		DeviceID:       "met-0101",
		AssetType:      domain.AssetSmartMeter,
		MQTTTopic:      "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry",
		Payload:        []byte(`{"voltage":232.1}`),
		EventTimestamp: time.Unix(1_745_500_002, 0),
		EdgeReceivedAt: time.Unix(1_745_500_003, 0),
	})
	if err != nil {
		t.Fatalf("enqueue second event: %v", err)
	}
	if second != 2 {
		t.Fatalf("second sequence = %d, want 2", second)
	}
}

func TestFilesystemReserveStopsWritesAndRaisesHealthState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := testConfig(filepath.Join(t.TempDir(), "queue.db"))
	cfg.MinFilesystemFreeBytes = math.MaxInt64
	q, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	defer q.Close()

	if _, err := q.Enqueue(ctx, testEvent([]byte("reading"), domain.PriorityCritical)); !errors.Is(err, domain.ErrFilesystemReserve) {
		t.Fatalf("enqueue below filesystem reserve error = %v, want ErrFilesystemReserve", err)
	}
	stats, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("read queue stats: %v", err)
	}
	if stats.State != domain.QueueFilesystemLow {
		t.Fatalf("queue state = %q, want %q", stats.State, domain.QueueFilesystemLow)
	}
	if stats.FilesystemFreeBytes <= 0 {
		t.Fatalf("filesystem free bytes = %d, want a positive measurement", stats.FilesystemFreeBytes)
	}
}

func TestFilesystemReserveIncludesIncomingWriteHeadroom(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := testConfig(filepath.Join(t.TempDir(), "queue.db"))
	cfg.MaxStorageBytes = 32 << 20
	q, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open queue for free-space measurement: %v", err)
	}
	stats, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("measure filesystem free space: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("close measured queue: %v", err)
	}

	cfg.MinFilesystemFreeBytes = stats.FilesystemFreeBytes - (1 << 20)
	q, err = sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen queue with filesystem reserve: %v", err)
	}
	defer q.Close()
	current, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("read current filesystem free space: %v", err)
	}
	if current.FilesystemFreeBytes <= cfg.MinFilesystemFreeBytes {
		t.Skip("filesystem free space changed too much to exercise projected-write guard")
	}
	if _, err := q.Enqueue(ctx, testEvent(make([]byte, 1<<20), domain.PriorityCritical)); !errors.Is(err, domain.ErrFilesystemReserve) {
		t.Fatalf("enqueue that would cross filesystem reserve error = %v, want ErrFilesystemReserve", err)
	}
}

func TestCapacityReservesSpaceForCriticalEvents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	q, err := sqlite.Open(ctx, sqlite.Config{
		Path:                   filepath.Join(t.TempDir(), "queue.db"),
		SiteID:                 "ng-kaji-01",
		GatewayID:              "gateway-01",
		MaxStorageBytes:        8 << 20,
		WarningPercent:         50,
		CriticalReservePercent: 20,
	})
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	defer q.Close()

	emptyStats, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("read empty queue stats: %v", err)
	}
	enqueueBytes(t, q, emptyStats.NormalAdmissionBytes, domain.PriorityNormal)
	if _, err := q.Enqueue(ctx, testEvent([]byte("x"), domain.PriorityNormal)); !errors.Is(err, domain.ErrCriticalReserve) {
		t.Fatalf("enqueue normal event error = %v, want ErrCriticalReserve", err)
	}

	stats, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("read queue stats: %v", err)
	}
	if stats.State != domain.QueueNormalSuspended {
		t.Fatalf("queue state = %q, want %q", stats.State, domain.QueueNormalSuspended)
	}
	if stats.Depth < 2 || stats.PayloadBytes != emptyStats.NormalAdmissionBytes {
		t.Fatalf("queue stats = depth %d, bytes %d", stats.Depth, stats.PayloadBytes)
	}

	enqueueBytes(t, q, emptyStats.CapacityBytes-emptyStats.NormalAdmissionBytes, domain.PriorityCritical)
	if _, err := q.Enqueue(ctx, testEvent([]byte("x"), domain.PriorityCritical)); !errors.Is(err, domain.ErrStorageCapacity) {
		t.Fatalf("enqueue beyond hard capacity error = %v, want ErrStorageCapacity", err)
	}
	fullStats, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("read full queue stats: %v", err)
	}
	if fullStats.DiskBytes > 8<<20 {
		t.Fatalf("queue disk bytes = %d, exceeds configured cap %d", fullStats.DiskBytes, 8<<20)
	}
	if _, err := q.Enqueue(ctx, testEvent(make([]byte, 500_000), domain.PriorityCritical)); !errors.Is(err, domain.ErrEventTooLarge) {
		t.Fatalf("enqueue oversized event error = %v, want ErrEventTooLarge", err)
	}
}

func TestStatsRaiseWarningAtConfiguredThreshold(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	q, err := sqlite.Open(ctx, sqlite.Config{
		Path:                   filepath.Join(t.TempDir(), "queue.db"),
		SiteID:                 "ng-kaji-01",
		GatewayID:              "gateway-01",
		MaxStorageBytes:        8 << 20,
		WarningPercent:         50,
		CriticalReservePercent: 20,
	})
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	defer q.Close()
	emptyStats, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("read empty queue stats: %v", err)
	}
	enqueueBytes(t, q, (emptyStats.CapacityBytes+1)/2, domain.PriorityNormal)
	warningStats, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("read warning queue stats: %v", err)
	}
	if warningStats.State != domain.QueueWarning {
		t.Fatalf("queue state = %q, want %q", warningStats.State, domain.QueueWarning)
	}
}

func TestFailedEventBlocksOnlyItsDeviceUntilRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	q := openTestQueue(t)
	defer q.Close()

	first, err := q.Enqueue(ctx, eventForDevice("met-01", []byte("first")))
	if err != nil {
		t.Fatalf("enqueue first device event: %v", err)
	}
	if _, err := q.Enqueue(ctx, eventForDevice("met-01", []byte("second"))); err != nil {
		t.Fatalf("enqueue second device event: %v", err)
	}
	third, err := q.Enqueue(ctx, eventForDevice("met-02", []byte("third")))
	if err != nil {
		t.Fatalf("enqueue other device event: %v", err)
	}

	retryAt := time.Now().Add(time.Hour).UTC()
	if err := q.MarkFailed(ctx, first, retryAt, "network unavailable"); err != nil {
		t.Fatalf("mark event failed: %v", err)
	}
	records, err := q.Ready(ctx, 10, 1<<20)
	if err != nil {
		t.Fatalf("read ready events: %v", err)
	}
	if len(records) != 1 || records[0].Sequence != third {
		t.Fatalf("ready sequences = %v, want only %d", sequences(records), third)
	}
}

func TestQueueRejectsChangedSiteOrGatewayIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	cfg := testConfig(path)
	q, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("close queue: %v", err)
	}

	cfg.GatewayID = "replacement-gateway"
	if _, err := sqlite.Open(ctx, cfg); !errors.Is(err, domain.ErrIdentityMismatch) {
		t.Fatalf("open with changed identity error = %v, want ErrIdentityMismatch", err)
	}
}

func openTestQueue(t *testing.T) *sqlite.Store {
	t.Helper()
	q, err := sqlite.Open(context.Background(), testConfig(filepath.Join(t.TempDir(), "queue.db")))
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	return q
}

func testConfig(path string) sqlite.Config {
	return sqlite.Config{
		Path:                   path,
		SiteID:                 "ng-kaji-01",
		GatewayID:              "gateway-01",
		MaxStorageBytes:        8 << 20,
		WarningPercent:         70,
		CriticalReservePercent: 10,
	}
}

func testEvent(payload []byte, priority domain.Priority) domain.Event {
	event := eventForDevice("met-0101", payload)
	event.Priority = priority
	return event
}

func eventForDevice(deviceID string, payload []byte) domain.Event {
	return domain.Event{
		DeviceID:       deviceID,
		AssetType:      domain.AssetSmartMeter,
		MQTTTopic:      "africa-west/ng-kaji-01/smartmeter/" + deviceID + "/telemetry",
		Payload:        payload,
		EventTimestamp: time.Unix(1_745_500_000, 0),
		EdgeReceivedAt: time.Unix(1_745_500_001, 0),
	}
}

func sequences(records []domain.Record) []uint64 {
	result := make([]uint64, len(records))
	for i := range records {
		result[i] = records[i].Sequence
	}
	return result
}

func enqueueBytes(t *testing.T, q *sqlite.Store, total int64, priority domain.Priority) {
	t.Helper()
	const chunkSize = int64(256 << 10)
	for total > 0 {
		size := min(total, chunkSize)
		if _, err := q.Enqueue(context.Background(), testEvent(make([]byte, size), priority)); err != nil {
			t.Fatalf("enqueue %d-byte chunk with priority %d: %v", size, priority, err)
		}
		total -= size
	}
}
