package adapter

import (
	"context"
	"fmt"
)

// SendGate carries Ledger's durable write; adapters call BeforeSend only after
// acquiring a ready transport. A zero gate denies.
type SendGate struct{ before func() error }

func NewSendGate(before func() error) SendGate { return SendGate{before: before} }

// AllowSend starts a command at the outer execution seam. It does not replace
// the ledger: production routes still install a durable gate before transport.
func AllowSend() SendGate { return NewSendGate(func() error { return nil }) }

func (g SendGate) BeforeSend(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return ContextError(err)
	}
	if g.before == nil {
		return fmt.Errorf("%w: send authorization is required", ErrInvalidCommand)
	}
	return g.before()
}

func (g SendGate) Then(before func() error) SendGate {
	return NewSendGate(func() error {
		if g.before == nil {
			return fmt.Errorf("%w: send authorization is required", ErrInvalidCommand)
		}
		if err := g.before(); err != nil {
			return err
		}
		return before()
	})
}
