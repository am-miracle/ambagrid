// implements opaque cursors and keyset pagination.
package page

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidCursor marks a cursor that cannot be decoded or applied.
var ErrInvalidCursor = errors.New("invalid cursor")

// Cursor fields may contain both the separator and escape character.
const (
	cursorVersion   = "v1"
	cursorSeparator = "\x1f"
	cursorEscape    = `\`
)

// Page contains one listing slice and the cursor for the next slice.
type Page[T any] struct {
	Items      []T
	Limit      int
	NextCursor string
}

// Encode packs the sort-key fields of the last row on a page into a token.
func Encode(fields ...string) string {
	parts := make([]string, 0, len(fields)+1)
	parts = append(parts, cursorVersion)
	for _, field := range fields {
		parts = append(parts, escapeField(field))
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, cursorSeparator)))
}

// Escape backslashes first to keep the encoding reversible.
func escapeField(field string) string {
	field = strings.ReplaceAll(field, cursorEscape, cursorEscape+cursorEscape)
	return strings.ReplaceAll(field, cursorSeparator, cursorEscape+"u")
}

func unescapeField(field string) string {
	var out strings.Builder
	out.Grow(len(field))
	for i := 0; i < len(field); i++ {
		if field[i] != cursorEscape[0] || i+1 >= len(field) {
			out.WriteByte(field[i])
			continue
		}
		switch field[i+1] {
		case 'u':
			out.WriteString(cursorSeparator)
		case cursorEscape[0]:
			out.WriteString(cursorEscape)
		default:
			// Preserve unknown escapes for compatibility with older cursors.
			out.WriteByte(field[i])
			continue
		}
		i++
	}
	return out.String()
}

// Decode returns exactly wantFields sort-key fields.
func Decode(token string, wantFields int) ([]string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: not base64url", ErrInvalidCursor)
	}

	parts := strings.Split(string(raw), cursorSeparator)
	if len(parts) != wantFields+1 {
		return nil, fmt.Errorf("%w: unexpected field count", ErrInvalidCursor)
	}
	if parts[0] != cursorVersion {
		return nil, fmt.Errorf("%w: unsupported version", ErrInvalidCursor)
	}
	fields := make([]string, 0, wantFields)
	for _, field := range parts[1:] {
		if field == "" {
			return nil, fmt.Errorf("%w: empty field", ErrInvalidCursor)
		}
		fields = append(fields, unescapeField(field))
	}

	return fields, nil
}

// Build trims a limit+1 query result and creates the next cursor.
func Build[T any](rows []T, limit int, key func(T) string) Page[T] {
	if len(rows) <= limit {
		return Page[T]{Items: rows, Limit: limit}
	}
	items := rows[:limit]
	return Page[T]{Items: items, Limit: limit, NextCursor: key(items[limit-1])}
}
