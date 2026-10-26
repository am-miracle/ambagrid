//go:build integration

package modbus_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/modbus"
)

// fakeServer is a minimal Modbus TCP server for function codes 3 and 4.
type fakeServer struct {
	listener net.Listener
	mu       sync.Mutex
	holding  map[uint16]uint16
	input    map[uint16]uint16
	// exception, when set, answers every request with this exception code.
	exception byte
	stall     chan struct{}
	entered   chan struct{}
}

func startServer(t *testing.T) *fakeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{listener: listener, holding: map[uint16]uint16{}, input: map[uint16]uint16{}}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *fakeServer) serve(conn net.Conn) {
	defer conn.Close()
	for {
		header := make([]byte, 7)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		pdu := make([]byte, int(binary.BigEndian.Uint16(header[4:]))-1)
		if _, err := io.ReadFull(conn, pdu); err != nil {
			return
		}
		s.mu.Lock()
		stall, exception := s.stall, s.exception
		image := s.holding
		if pdu[0] == 4 {
			image = s.input
		}
		var reply []byte
		if exception != 0 {
			reply = []byte{pdu[0] | 0x80, exception}
		} else {
			address, quantity := binary.BigEndian.Uint16(pdu[1:]), binary.BigEndian.Uint16(pdu[3:])
			reply = []byte{pdu[0], byte(2 * quantity)}
			for i := uint16(0); i < quantity; i++ {
				word, ok := image[address+i]
				if !ok {
					reply = []byte{pdu[0] | 0x80, 2}
					break
				}
				reply = binary.BigEndian.AppendUint16(reply, word)
			}
		}
		s.mu.Unlock()
		if stall != nil {
			if s.entered != nil {
				s.entered <- struct{}{}
			}
			<-stall
			return
		}
		binary.BigEndian.PutUint16(header[4:], uint16(len(reply)+1))
		if _, err := conn.Write(append(header, reply...)); err != nil {
			return
		}
	}
}

func TestTCPClientCancellationDuringReadAndWhileQueued(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("queued=%t/deadline=%t", queued, deadline), func(t *testing.T) {
				s := startServer(t)
				s.stall = make(chan struct{})
				s.entered = make(chan struct{}, 2)
				t.Cleanup(func() { close(s.stall) })
				c := dial(t, s)
				if queued {
					first, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan struct{})
					go func() { defer close(done); _, _ = c.ReadRegisters(first, 1, modbus.Holding, 1, 1) }()
					defer func() { cancel(); <-done }()
					<-s.entered
				}
				ctx, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
					want = adapter.ErrTimeout
				}
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := c.ReadRegisters(ctx, 2, modbus.Holding, 1, 1); done <- err }()
				if !queued {
					<-s.entered
				}
				if !deadline {
					if queued {
						time.AfterFunc(20*time.Millisecond, cancel)
					} else {
						cancel()
					}
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("read error = %v, want %v", err, want)
					}
				case <-time.After(250 * time.Millisecond):
					t.Error("read did not return promptly after cancellation/deadline")
					<-done
				}
			})
		}
	}
}

func dial(t *testing.T, s *fakeServer) *modbus.TCPClient {
	t.Helper()
	c := modbus.NewTCPClient(s.listener.Addr().String(), time.Second)
	t.Cleanup(func() { c.Close() })
	return c
}

func TestTCPClientReadsHoldingAndInputRegisters(t *testing.T) {
	s := startServer(t)
	s.holding[40] = 0x4248
	s.holding[41] = 0x0000
	s.input[7] = 1234
	c := dial(t, s)
	ctx := context.Background()

	words, err := c.ReadRegisters(ctx, 1, modbus.Holding, 40, 2)
	if err != nil || len(words) != 2 || math.Float32frombits(uint32(words[0])<<16|uint32(words[1])) != 50 {
		t.Fatalf("holding words = %v, %v", words, err)
	}
	if words, err := c.ReadRegisters(ctx, 1, modbus.Input, 7, 1); err != nil || len(words) != 1 || words[0] != 1234 {
		t.Fatalf("input words = %v, %v", words, err)
	}
	if _, err := c.ReadRegisters(ctx, 1, modbus.Input, 40, 1); !errors.Is(err, modbus.ErrRegisterUnavailable) {
		t.Fatalf("input read of a holding-only address: %v, want ErrRegisterUnavailable", err)
	}
}

func TestTCPClientMapsExceptionsAndTimeouts(t *testing.T) {
	s := startServer(t)
	s.exception = 11
	c := dial(t, s)
	if _, err := c.ReadRegisters(context.Background(), 225, modbus.Holding, 259, 1); !errors.Is(err, adapter.ErrDeviceRejected) {
		t.Fatalf("gateway target exception: %v, want ErrDeviceRejected", err)
	}

	stalled := startServer(t)
	stalled.holding[259] = 5234
	stalled.stall = make(chan struct{})
	t.Cleanup(func() { close(stalled.stall) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := dial(t, stalled).ReadRegisters(ctx, 225, modbus.Holding, 259, 1); !errors.Is(err, adapter.ErrTimeout) {
		t.Fatalf("stalled server: %v, want ErrTimeout", err)
	}
}

func TestAdapterReadsVictronBatteryOverTCP(t *testing.T) {
	s := startServer(t)
	for address, word := range map[uint16]uint16{259: 5234, 261: 65413, 266: 725} {
		s.holding[address] = word
	}
	a, err := modbus.New(modbus.Config{Map: victron(t), Device: battery, UnitID: 225}, dial(t, s))
	if err != nil {
		t.Fatal(err)
	}
	readings, err := a.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := readings[0]
	if !near(r.Metrics.Voltage, 52.34) || !near(r.Metrics.Current, -12.3) || !r.InternalTemperatureMissing || math.Abs(r.BatterySOCPct-72.5) > 1e-9 {
		t.Fatalf("reading = %+v", r)
	}
}

func TestTCPClientDistinguishesProtocolTimeoutFromCallerDeadline(t *testing.T) {
	s := startServer(t)
	s.stall = make(chan struct{})
	blocked := s.stall
	t.Cleanup(func() { close(blocked) })
	client := modbus.NewTCPClient(s.listener.Addr().String(), 30*time.Millisecond)
	t.Cleanup(func() { client.Close() })
	if _, err := client.ReadRegisters(context.Background(), 1, modbus.Holding, 1, 1); !errors.Is(err, adapter.ErrProtocolTimeout) || !errors.Is(err, adapter.ErrTimeout) {
		t.Fatalf("protocol timeout: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := client.ReadRegisters(ctx, 1, modbus.Holding, 1, 1); !errors.Is(err, adapter.ErrTimeout) || errors.Is(err, adapter.ErrProtocolTimeout) {
		t.Fatalf("caller timeout classified as protocol: %v", err)
	}
	s.mu.Lock()
	s.stall = nil
	s.holding[1] = 230
	s.mu.Unlock()
	if words, err := client.ReadRegisters(context.Background(), 1, modbus.Holding, 1, 1); err != nil || len(words) != 1 || words[0] != 230 {
		t.Fatalf("read after timeouts did not reconnect cleanly: %v %v", words, err)
	}
}
