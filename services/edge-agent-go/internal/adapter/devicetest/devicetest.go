// devicetest gives adapter test doubles the controls the adaptertest suite
// drives. It does not import testing, so devices built into the binary can use it.
package devicetest

import (
	"context"
	"sync"
)

// Gate is called at the start of every device exchange. Its zero value lets
// every exchange through.
type Gate struct {
	mu       sync.Mutex
	stall    chan struct{}
	hold     *hold
	arrivals int
}

type hold struct {
	entered  chan struct{}
	released chan struct{}
}

func (g *Gate) Enter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	g.arrivals++
	stall, held := g.stall, g.hold
	g.stall, g.hold = nil, nil
	g.mu.Unlock()

	switch {
	case stall != nil:
		close(stall)
		<-ctx.Done()
		return ctx.Err()
	case held != nil:
		close(held.entered)
		select {
		case <-held.released:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Stall makes the next exchange block until its context ends. The returned
// channel closes once an exchange is blocked inside the stall.
func (g *Gate) Stall() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stall = make(chan struct{})
	return g.stall
}

// Hold makes the next exchange block until release is called, so a test can
// act while that exchange is known to be in flight.
func (g *Gate) Hold() (entered <-chan struct{}, release func()) {
	h := &hold{entered: make(chan struct{}), released: make(chan struct{})}
	g.mu.Lock()
	g.hold = h
	g.mu.Unlock()
	var once sync.Once
	return h.entered, func() { once.Do(func() { close(h.released) }) }
}

// Arrivals counts exchanges that reached the device, including stalled ones.
func (g *Gate) Arrivals() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.arrivals
}

// Recorder keeps the vendor payloads a device applied.
type Recorder struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (r *Recorder) Record(payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payloads = append(r.payloads, payload)
}

func (r *Recorder) Received() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.payloads...)
}
