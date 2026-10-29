package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	gridx "github.com/grid-x/modbus"

	"edge-agent-go/internal/adapter"
)

// TCPClient is a Client over Modbus TCP. A gateway such as a Cerbo GX serves
// several unit IDs on one connection, so requests are serialized.
type TCPClient struct {
	slot    chan struct{}
	handler *gridx.TCPClientHandler
	client  gridx.Client
	timeout time.Duration
	connMu  sync.Mutex
	conn    net.Conn
}

func NewTCPClient(address string, timeout time.Duration) *TCPClient {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	c := &TCPClient{slot: make(chan struct{}, 1), timeout: timeout}
	c.handler = gridx.NewTCPClientHandler(address, gridx.WithDialer(c.dial))
	c.handler.Timeout = timeout
	c.client = gridx.NewClient(c.handler)
	return c
}

func (c *TCPClient) dial(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: c.timeout}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	c.connMu.Lock()
	defer c.connMu.Unlock()
	// Cancellation may have run before the new connection was available.
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	c.conn = conn
	return conn, nil
}

func (c *TCPClient) ReadRegisters(ctx context.Context, unit byte, function Function, address, quantity uint16) ([]uint16, error) {
	if err := ctx.Err(); err != nil {
		return nil, adapter.ContextError(err)
	}
	select {
	case c.slot <- struct{}{}:
		defer func() { <-c.slot }()
	case <-ctx.Done():
		return nil, adapter.ContextError(ctx.Err())
	}
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, adapter.ContextError(err)
	}
	// grid-x bounds socket I/O by handler.Timeout, but does not interrupt it
	// when ctx ends. Close the raw connection, not handler.Close (which waits
	// for grid-x's exchange lock). A later read then dials a fresh stream.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(interrupted)
		c.connMu.Lock()
		defer c.connMu.Unlock()
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
	defer func() {
		if !stop() {
			<-interrupted // never let this callback close the next read's socket
		}
	}()
	c.handler.SetSlave(unit)
	var raw []byte
	var err error
	switch function {
	case Holding:
		raw, err = c.client.ReadHoldingRegisters(ctx, address, quantity)
	case Input:
		raw, err = c.client.ReadInputRegisters(ctx, address, quantity)
	default:
		return nil, fmt.Errorf("%w: function %q", adapter.ErrUnsupported, function)
	}
	if err != nil {
		// A timeout may leave grid-x caching a stream our cancellation
		// callback has closed. Reset it before the next read reconnects.
		_ = c.handler.Close()
		if adapter.ProtocolDeadlineExceeded(caller, ctx) {
			return nil, fmt.Errorf("%w: %w", adapter.ErrProtocolTimeout, err)
		}
		return nil, classify(caller, err)
	}
	if len(raw) != 2*int(quantity) {
		return nil, fmt.Errorf("%w: %d bytes for %d registers", adapter.ErrInvalidResponse, len(raw), quantity)
	}
	words := make([]uint16, quantity)
	for i := range words {
		words[i] = binary.BigEndian.Uint16(raw[2*i:])
	}
	return words, nil
}

func (c *TCPClient) Close() error {
	return c.handler.Close()
}

func classify(ctx context.Context, err error) error {
	var exception *gridx.Error
	if errors.As(err, &exception) {
		switch exception.ExceptionCode {
		case gridx.ExceptionCodeIllegalFunction, gridx.ExceptionCodeIllegalDataAddress:
			return fmt.Errorf("%w: %w", ErrRegisterUnavailable, err)
		}
		return fmt.Errorf("%w: %w", adapter.ErrDeviceRejected, err)
	}
	if ctx.Err() != nil {
		return adapter.ContextError(fmt.Errorf("%w: %w", ctx.Err(), err))
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: %w", adapter.ErrProtocolTimeout, err)
	}
	return err
}
