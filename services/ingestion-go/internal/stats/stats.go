package stats

import "sync/atomic"

// safe to update from MQTT callbacks and producer workers.
type IngestionStats struct {
	received  atomic.Int64
	produced  atomic.Int64
	dropped   atomic.Int64
	failed    atomic.Int64
	dlqFailed atomic.Int64
}

func (s *IngestionStats) AddReceived() int64 { return s.received.Add(1) }
func (s *IngestionStats) Received() int64    { return s.received.Load() }

func (s *IngestionStats) AddProduced() int64 { return s.produced.Add(1) }
func (s *IngestionStats) Produced() int64    { return s.produced.Load() }

func (s *IngestionStats) AddDropped() int64 { return s.dropped.Add(1) }
func (s *IngestionStats) Dropped() int64    { return s.dropped.Load() }

func (s *IngestionStats) AddFailed() int64 { return s.failed.Add(1) }
func (s *IngestionStats) Failed() int64    { return s.failed.Load() }

func (s *IngestionStats) AddDLQFailed() int64 { return s.dlqFailed.Add(1) }
func (s *IngestionStats) DLQFailed() int64    { return s.dlqFailed.Load() }
