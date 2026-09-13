// validates identifiers accepted by the domain.
package domain

import (
	"errors"
	"fmt"
)

// ErrInvalidID marks an identifier rejected before querying Postgres.
var ErrInvalidID = errors.New("invalid identifier")

// ValidateAlertID checks the UUID form used by Postgres.
func ValidateAlertID(value string) error {
	if len(value) != 36 {
		return fmt.Errorf("%w: alert_id must be a UUID", ErrInvalidID)
	}
	for i, char := range value {
		switch i {
		case 8, 13, 18, 23:
			if char != '-' {
				return fmt.Errorf("%w: alert_id must be a UUID", ErrInvalidID)
			}
		default:
			isHex := (char >= '0' && char <= '9') ||
				(char >= 'a' && char <= 'f') ||
				(char >= 'A' && char <= 'F')
			if !isHex {
				return fmt.Errorf("%w: alert_id must be a UUID", ErrInvalidID)
			}
		}
	}
	return nil
}
