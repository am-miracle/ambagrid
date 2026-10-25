package victron

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"edge-agent-go/internal/adapter"
)

// TCP opens a bounded exchange per register. No connection or transaction state
// is shared between concurrent reads. Configure Address as host:502.
type TCP struct {
	Address  string
	Timeout  time.Duration
	sequence atomic.Uint32
}

func (c *TCP) ReadHolding(ctx context.Context, unit byte, address uint16) (value uint16, err error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = adapter.ContextError(ctx.Err())
		}
	}()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	tx := uint16(c.sequence.Add(1))
	request := make([]byte, 12)
	binary.BigEndian.PutUint16(request, tx)
	binary.BigEndian.PutUint16(request[4:], 6)
	request[6], request[7] = unit, 3
	binary.BigEndian.PutUint16(request[8:], address)
	binary.BigEndian.PutUint16(request[10:], 1)
	if _, err = io.Copy(conn, bytes.NewReader(request)); err != nil {
		return 0, transportError(err)
	}
	header := make([]byte, 7)
	if _, err = io.ReadFull(conn, header); err != nil {
		return 0, transportError(err)
	}
	length := binary.BigEndian.Uint16(header[4:])
	if binary.BigEndian.Uint16(header) != tx || binary.BigEndian.Uint16(header[2:]) != 0 || header[6] != unit || length < 3 || length > 254 {
		return 0, fmt.Errorf("%w: invalid Modbus header", adapter.ErrInvalidResponse)
	}
	pdu := make([]byte, int(length)-1)
	if _, err = io.ReadFull(conn, pdu); err != nil {
		return 0, transportError(err)
	}
	if pdu[0] == 0x83 && len(pdu) == 2 {
		switch pdu[1] {
		case 1, 2:
			return 0, fmt.Errorf("%w: Modbus exception %d", adapter.ErrUnsupported, pdu[1])
		case 10, 11:
			return 0, fmt.Errorf("%w: Modbus gateway exception %d", adapter.ErrDeviceRejected, pdu[1])
		default:
			return 0, fmt.Errorf("%w: Modbus exception %d", adapter.ErrDeviceRejected, pdu[1])
		}
	}
	if len(pdu) != 4 || pdu[0] != 3 || pdu[1] != 2 {
		return 0, fmt.Errorf("%w: invalid Modbus register response", adapter.ErrInvalidResponse)
	}
	return binary.BigEndian.Uint16(pdu[2:]), nil
}
func transportError(err error) error {
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return fmt.Errorf("%w: %w", adapter.ErrTimeout, err)
	}
	return err
}
