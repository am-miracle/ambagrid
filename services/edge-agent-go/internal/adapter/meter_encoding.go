package adapter

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
)

// MeterCommandPayload is the shared meter operation format. Simulator links
// carry Ref in JSON; MQTT carries it as correlation data instead.
type MeterCommandPayload struct {
	Op     string `json:"op"`
	Ref    string `json:"ref,omitempty"`
	Device string `json:"device"`
	Wh     int64  `json:"wh,omitempty"`
	Closed *bool  `json:"closed,omitempty"`
	Target string `json:"target,omitempty"`
}

// EncodeMeterCommand expects a validated command. Transport-specific envelopes
// do not decide relay polarity or convert credit units independently.
func EncodeMeterCommand(cmd Command) MeterCommandPayload {
	payload := MeterCommandPayload{Ref: cmd.ID, Device: cmd.DeviceID}
	switch cmd.Type {
	case CommandCredit:
		payload.Op = "credit"
		payload.Wh = int64(math.Round(cmd.CreditKWh * 1000))
	case CommandDisconnect, CommandReconnect:
		closed := cmd.Type == CommandReconnect
		payload.Op = "relay"
		payload.Closed = &closed
	case CommandRequestStatus:
		payload.Op = "status"
	}
	return payload
}

var templatePlaceholder = regexp.MustCompile(`\{[a-z_]+\}`)

// RenderCommandTemplate substitutes JSON values, so placeholders stand unquoted
// in the template: {"meter":{device},"wh":{wh}}. Credit templates also get {wh}.
func RenderCommandTemplate(template string, cmd Command) ([]byte, error) {
	values := map[string]any{"{device}": cmd.DeviceID}
	if cmd.Type == CommandCredit {
		values["{wh}"] = EncodeMeterCommand(cmd).Wh
	}
	var renderErr error
	rendered := templatePlaceholder.ReplaceAllStringFunc(template, func(name string) string {
		value, ok := values[name]
		if !ok {
			renderErr = fmt.Errorf("unknown placeholder %s for %s", name, cmd.Type)
			return name
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			renderErr = err
		}
		return string(encoded)
	})
	if renderErr != nil {
		return nil, renderErr
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(rendered), &object); err != nil {
		return nil, fmt.Errorf("%s template is not a JSON object: %w", cmd.Type, err)
	}
	return []byte(rendered), nil
}
