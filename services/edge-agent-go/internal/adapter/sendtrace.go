package adapter

import "context"

type sendHookKey struct{}

// WithSendHook returns a context whose BeforeSend runs hook after any hook
// already installed, in the style of net/http/httptrace.
func WithSendHook(ctx context.Context, hook func() error) context.Context {
	if prior, ok := ctx.Value(sendHookKey{}).(func() error); ok {
		next := hook
		hook = func() error {
			if err := prior(); err != nil {
				return err
			}
			return next()
		}
	}
	return context.WithValue(ctx, sendHookKey{}, hook)
}

// BeforeSend must be called by adapters immediately before transmitting a
// command. An error means the command must not be sent.
func BeforeSend(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return ContextError(err)
	}
	if hook, ok := ctx.Value(sendHookKey{}).(func() error); ok {
		return hook()
	}
	return nil
}
