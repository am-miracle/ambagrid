package fallback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type gsmSeptetFixture struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Repeat  int    `json:"repeat"`
	Septets int    `json:"septets"`
}

func TestGSMSeptetsConformsToSMSV1Contract(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "..", "proto", "fixtures", "sms_v1_septets.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read GSM-7 contract fixture: %v", err)
	}

	var fixtures []gsmSeptetFixture
	if err := json.Unmarshal(contents, &fixtures); err != nil {
		t.Fatalf("decode GSM-7 contract fixture: %v", err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			value := strings.Repeat(fixture.Value, fixture.Repeat)
			if got := gsmSeptets(value); got != fixture.Septets {
				t.Fatalf("gsmSeptets(%q) = %d, want %d", value, got, fixture.Septets)
			}
		})
	}
}
