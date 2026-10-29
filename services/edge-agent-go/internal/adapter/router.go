package adapter

import (
	"context"
	"fmt"
)

// Router selects an already ledger-protected executor by exact device ID.
// Unknown devices never fall back to another device's capabilities or transport.
type Router struct{ routes map[string]CommandAdapter }

func NewRouter(routes map[string]CommandAdapter) *Router {
	owned := make(map[string]CommandAdapter, len(routes))
	for id, executor := range routes {
		owned[id] = executor
	}
	return &Router{owned}
}

func (r *Router) Execute(ctx context.Context, cmd Command) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, ContextError(err)
	}
	route, ok := r.routes[cmd.DeviceID]
	if !ok {
		return CommandResult{}, fmt.Errorf("%w: no command adapter for device %q", ErrUnsupported, cmd.DeviceID)
	}
	return route.Execute(ctx, cmd)
}
