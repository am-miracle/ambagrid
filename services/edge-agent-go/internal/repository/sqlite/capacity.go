package sqlite

import (
	"errors"
	"math"
	"os"

	"edge-agent-go/internal/domain"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func (s *Store) normalAdmissionLimit() int64 {
	return s.payloadCapacity() * int64(100-s.cfg.CriticalReservePercent) / 100
}

func (s *Store) payloadCapacity() int64 {
	return s.cfg.MaxStorageBytes * 65 / 100
}

func (s *Store) databaseBudget() int64 {
	return s.cfg.MaxStorageBytes * 80 / 100
}

func walBudget(maxStorageBytes int64) int64 {
	return maxStorageBytes * 5 / 100
}

func (s *Store) writeHeadroom(event domain.Event) int64 {
	recordBytes := int64(len(event.Payload) + len(event.DeviceID) + len(event.MQTTTopic) + 64)
	return 2 * (recordBytes + 2*s.pageSize)
}

func filesystemFreeBytes(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	available := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blockSize > 0 && available > math.MaxInt64/blockSize {
		return math.MaxInt64, nil
	}
	return int64(available * blockSize), nil
}

func queueDiskBytes(path string) (int64, error) {
	var bytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err == nil {
			bytes += info.Size()
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
	}
	return bytes, nil
}

func isSQLiteFull(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_FULL
}
