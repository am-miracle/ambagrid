// Validates requested page sizes.
package services

import (
	"errors"
	"fmt"
)

// ErrInvalidRequest marks service input errors safe for clients.
var ErrInvalidRequest = errors.New("invalid request")

// PageLimits defines default and maximum listing sizes.
type PageLimits struct {
	DefaultSize int
	MaxSize     int
}

// Resolve applies the default and rejects sizes above the maximum.
func (l PageLimits) Resolve(requested int) (int, error) {
	switch {
	case requested == 0:
		return l.DefaultSize, nil
	case requested < 0:
		return 0, fmt.Errorf("%w: limit must be greater than zero", ErrInvalidRequest)
	case requested > l.MaxSize:
		return 0, fmt.Errorf("%w: limit must not exceed %d", ErrInvalidRequest, l.MaxSize)
	default:
		return requested, nil
	}
}
