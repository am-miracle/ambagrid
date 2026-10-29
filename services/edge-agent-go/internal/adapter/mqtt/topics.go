package mqtt

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	DefaultCommandTopic = "site/{site_id}/meter/{device_id}/cmd"
	DefaultReplyTopic   = "site/{site_id}/meter/{device_id}/cmd/reply"
)

var placeholder = regexp.MustCompile(`\{[^}]*\}`)

// Topics is the configured topic map. Replies are per device, so broker access
// rules can stop one device answering for another.
type Topics struct {
	command, reply string
}

// NewTopics fills {site_id} now and leaves {device_id} for each command; the
// device must be a whole topic level so the reply filter can use "+".
func NewTopics(command, reply, siteID string) (Topics, error) {
	if siteID == "" || strings.ContainsAny(siteID, "/+#") {
		return Topics{}, fmt.Errorf("site ID %q cannot be used in an MQTT topic", siteID)
	}
	var t Topics
	for _, target := range []struct {
		template string
		into     *string
	}{{command, &t.command}, {reply, &t.reply}} {
		if err := validateTemplate(target.template); err != nil {
			return Topics{}, fmt.Errorf("topic template %q: %w", target.template, err)
		}
		*target.into = strings.ReplaceAll(target.template, "{site_id}", siteID)
	}
	// Compare the expanded filters too: different templates can name the
	// same topic once the site or a device ID is substituted.
	commands, replies := strings.Split(t.Command("+"), "/"), strings.Split(t.ReplyFilter(), "/")
	if len(commands) == len(replies) {
		overlap := true
		for i, level := range commands {
			if level != "+" && replies[i] != "+" && level != replies[i] {
				overlap = false
				break
			}
		}
		if overlap {
			return Topics{}, fmt.Errorf("command and reply topics must differ for every device")
		}
	}
	return t, nil
}

func validateTemplate(template string) error {
	if strings.ContainsAny(template, "+#") {
		return fmt.Errorf("must not contain wildcard characters")
	}
	for _, name := range placeholder.FindAllString(template, -1) {
		if name != "{site_id}" && name != "{device_id}" {
			return fmt.Errorf("unknown placeholder %s; use {site_id} and {device_id}", name)
		}
	}
	levels := strings.Split(template, "/")
	devices := 0
	for _, level := range levels {
		if strings.Contains(level, "{device_id}") {
			if level != "{device_id}" {
				return fmt.Errorf("{device_id} must fill a whole topic level")
			}
			devices++
		}
	}
	switch devices {
	case 0:
		return fmt.Errorf("must contain {device_id}")
	case 1:
		return nil
	}
	return fmt.Errorf("must contain {device_id} once")
}

func (t Topics) Command(deviceID string) string {
	return strings.ReplaceAll(t.command, "{device_id}", deviceID)
}

func (t Topics) Reply(deviceID string) string {
	return strings.ReplaceAll(t.reply, "{device_id}", deviceID)
}

func (t Topics) ReplyFilter() string {
	return t.Reply("+")
}

// matches reports whether an MQTT topic filter matches a topic name.
func matches(filter, topic string) bool {
	f, n := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, level := range f {
		if level == "#" {
			return true
		}
		if i >= len(n) || (level != "+" && level != n[i]) {
			return false
		}
	}
	return len(f) == len(n)
}
