package adapter

import (
	"context"
	"sync"
)

// Slot serializes a transport that carries one exchange at a time, such as a
// serial or Modbus link, while letting queued callers give up when their context ends.
type Slot struct {
	ch chan struct{}
}

func NewSlot() *Slot {
	return &Slot{ch: make(chan struct{}, 1)}
}

func (s *Slot) Acquire(ctx context.Context) (release func(), err error) {
	select {
	case s.ch <- struct{}{}:
	case <-ctx.Done():
		return nil, ContextError(ctx.Err())
	}
	// select picks randomly when the slot and ctx.Done are both ready.
	if err := ctx.Err(); err != nil {
		<-s.ch
		return nil, ContextError(err)
	}
	// Once-guarded: a second release would otherwise free another caller's slot.
	var once sync.Once
	return func() { once.Do(func() { <-s.ch }) }, nil
}
