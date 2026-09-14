package postgres

import (
	"errors"
	"testing"

	"api-go/internal/domain"
)

func TestParseEnumRejectsUnknownStoredValue(t *testing.T) {
	_, err := parseEnum("asset-1", domain.ParseAssetType, "not_a_real_type")
	if !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("err = %v, want wrapped domain.ErrInvalidData", err)
	}
}

func TestParseEnumAcceptsKnownStoredValue(t *testing.T) {
	got, err := parseEnum("asset-1", domain.ParseAssetType, string(domain.AssetTypeSmartMeter))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != domain.AssetTypeSmartMeter {
		t.Fatalf("got = %v, want %v", got, domain.AssetTypeSmartMeter)
	}
}
