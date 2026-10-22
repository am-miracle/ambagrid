package fallback

import "testing"

func TestRedactOutput(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "error: number=+2348012345678 not reachable",
			want:  "error: number=234***78 not reachable",
		},
		{
			input: "SMS/12 created for 08012345678",
			want:  "SMS/12 created for 080***78",
		},
		{
			input: "no phone number here",
			want:  "no phone number here",
		},
		{
			input: "error code 42",
			want:  "error code 42",
		},
	}
	for _, tt := range tests {
		got := redactOutput(tt.input)
		if got != tt.want {
			t.Errorf("redactOutput(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
