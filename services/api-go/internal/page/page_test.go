// Tests cursor encoding, decoding, and page construction.
package page

import (
	"errors"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	fields, err := Decode(Encode("2026-09-03T10:00:00Z", "met-0104"), 2)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if fields[0] != "2026-09-03T10:00:00Z" || fields[1] != "met-0104" {
		t.Fatalf("Decode() = %v, want the encoded fields", fields)
	}
}

// Free-form device IDs may contain cursor control characters.
func TestEncodeSurvivesSeparatorsInFields(t *testing.T) {
	tests := map[string]string{
		"separator":        "met-0104\x1fspoofed",
		"escape character": `met-0104\spoofed`,
		"escape sequence":  `met-0104\uspoofed`,
		"trailing escape":  `met-0104\`,
	}

	for name, assetID := range tests {
		t.Run(name, func(t *testing.T) {
			fields, err := Decode(Encode(assetID), 1)
			if err != nil {
				t.Fatalf("Decode(Encode(%q)) error = %v", assetID, err)
			}
			if fields[0] != assetID {
				t.Fatalf("round trip = %q, want %q", fields[0], assetID)
			}
		})
	}
}

func TestEncodeKeepsFieldsApartWhenOneContainsASeparator(t *testing.T) {
	// A cursor from another listing must fail the arity check.
	fields, err := Decode(Encode("2026-09-03T10:00:00Z", "met\x1f0104"), 2)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if fields[1] != "met\x1f0104" {
		t.Fatalf("second field = %q, want the separator preserved inside it", fields[1])
	}

	if _, err := Decode(Encode("a\x1fb"), 2); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("a one-field cursor decoded as two fields, want ErrInvalidCursor, got %v", err)
	}
}

func TestDecodeRejectsUnusableCursors(t *testing.T) {
	tests := map[string]struct {
		token      string
		wantFields int
	}{
		"not base64":            {token: "not a cursor!", wantFields: 1},
		"wrong field count":     {token: Encode("a", "b"), wantFields: 1},
		"empty field":           {token: Encode(""), wantFields: 1},
		"unsupported version":   {token: "djk5AWFzc2V0", wantFields: 1},
		"cursor from other API": {token: "aGVsbG8", wantFields: 1},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(test.token, test.wantFields); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("Decode() error = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestBuildKeepsTheExtraRowOutOfThePage(t *testing.T) {
	rows := []string{"a", "b", "c"}

	result := Build(rows, 2, func(row string) string { return Encode(row) })

	if len(result.Items) != 2 {
		t.Fatalf("Items = %v, want the first two rows", result.Items)
	}
	if result.Limit != 2 {
		t.Fatalf("Limit = %d, want 2", result.Limit)
	}

	fields, err := Decode(result.NextCursor, 1)
	if err != nil {
		t.Fatalf("Decode(NextCursor) error = %v", err)
	}
	// The cursor points to the last returned row, not the over-fetched row.
	if fields[0] != "b" {
		t.Fatalf("NextCursor points at %q, want %q", fields[0], "b")
	}
}

func TestBuildReportsNoNextPageWhenRowsAreExhausted(t *testing.T) {
	result := Build([]string{"a", "b"}, 2, func(row string) string { return Encode(row) })

	if result.NextCursor != "" {
		t.Fatalf("NextCursor = %q, want empty", result.NextCursor)
	}
}
