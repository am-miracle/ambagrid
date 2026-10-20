package collector

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"edge-agent-go/internal/domain"
)

func normalize(siteID, region string, reading domain.Reading, receivedAt time.Time) (domain.Event, error) {
	if strings.TrimSpace(reading.DeviceID) == "" {
		return domain.Event{}, errors.New("reading device ID must not be empty")
	}
	if !reading.AssetType.Valid() {
		return domain.Event{}, fmt.Errorf("reading asset type %q is invalid", reading.AssetType)
	}
	if reading.TakenAt.IsZero() {
		return domain.Event{}, errors.New("reading timestamp is required")
	}
	if len(reading.Payload) == 0 {
		return domain.Event{}, errors.New("reading payload must not be empty")
	}
	return domain.Event{
		DeviceID:       reading.DeviceID,
		AssetType:      reading.AssetType,
		MQTTTopic:      fmt.Sprintf("%s/%s/%s/%s/telemetry", region, siteID, reading.AssetType.TopicSegment(), reading.DeviceID),
		Payload:        reading.Payload,
		Priority:       domain.PriorityNormal,
		EventTimestamp: reading.TakenAt.UTC(),
		EdgeReceivedAt: receivedAt.UTC(),
	}, nil
}
