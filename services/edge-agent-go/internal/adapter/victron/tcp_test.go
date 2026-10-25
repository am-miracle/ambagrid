package victron_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/victron"
)

func TestTCPRegistersAndExceptions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply []byte
		want  uint16
		err   error
	}{
		{name: "Register", reply: []byte{3, 2, 0x14, 0x72}, want: 5234},
		{name: "UnsupportedRegister", reply: []byte{0x83, 2}, err: adapter.ErrUnsupported},
		{name: "MissingDevice", reply: []byte{0x83, 11}, err: adapter.ErrDeviceRejected},
		{name: "Malformed", reply: []byte{4, 2, 0x14, 0x72}, err: adapter.ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			defer func() { <-done }()
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				request := make([]byte, 12)
				if _, err := io.ReadFull(conn, request); err != nil {
					t.Error(err)
					return
				}
				if request[6] != 225 || request[7] != 3 || binary.BigEndian.Uint16(request[8:]) != 259 || binary.BigEndian.Uint16(request[10:]) != 1 {
					t.Errorf("request=%x", request)
				}
				header := append([]byte(nil), request[:7]...)
				binary.BigEndian.PutUint16(header[4:], uint16(len(tc.reply)+1))
				conn.Write(append(header, tc.reply...))
			}()
			client := &victron.TCP{Address: listener.Addr().String(), Timeout: time.Second}
			got, err := client.ReadHolding(context.Background(), 225, 259)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got=%d err=%v want=%d err=%v", got, err, tc.want, tc.err)
			}
		})
	}
}
