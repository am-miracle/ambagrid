package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type prunerStub struct {
	results []int64
	err     error
	limits  []int
	cancel  context.CancelFunc
}

func (p *prunerStub) PruneCommands(_ context.Context, _ time.Time, limit int) (int64, error) {
	p.limits = append(p.limits, limit)
	if len(p.limits) == len(p.results) {
		p.cancel()
	}
	return p.results[len(p.limits)-1], p.err
}

func TestRunCommandPruneBatchesUntilAShortBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &prunerStub{results: []int64{commandPruneBatch, commandPruneBatch, 3}, cancel: cancel}

	runCommandPrune(ctx, store, time.Hour, time.Millisecond)

	if len(store.limits) != 3 || store.limits[0] != commandPruneBatch {
		t.Fatalf("limits = %v, want three of %d", store.limits, commandPruneBatch)
	}
}

func TestRunCommandPruneStopsTheCycleOnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &prunerStub{results: []int64{commandPruneBatch}, err: errors.New("disk full"), cancel: cancel}

	runCommandPrune(ctx, store, time.Hour, time.Millisecond)

	if len(store.limits) != 1 {
		t.Fatalf("prune calls = %d, want 1 after an error", len(store.limits))
	}
}
