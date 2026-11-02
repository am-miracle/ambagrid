package mqtt_test

import (
	"strings"
	"testing"

	"edge-agent-go/internal/adapter/mqtt"
)

func TestTopicsRenderTheConfiguredTemplates(t *testing.T) {
	topics, err := mqtt.NewTopics("site/{site_id}/meter/{device_id}/cmd", "site/{site_id}/meter/{device_id}/cmd/reply", "ng-kaji-01")
	if err != nil {
		t.Fatal(err)
	}
	if got := topics.Command("meter-01"); got != "site/ng-kaji-01/meter/meter-01/cmd" {
		t.Errorf("command topic = %q", got)
	}
	if got := topics.Reply("meter-01"); got != "site/ng-kaji-01/meter/meter-01/cmd/reply" {
		t.Errorf("reply topic = %q", got)
	}
	if got := topics.ReplyFilter(); got != "site/ng-kaji-01/meter/+/cmd/reply" {
		t.Errorf("reply filter = %q", got)
	}
}

func TestTopicsRejectUnsafeTemplates(t *testing.T) {
	for name, c := range map[string]struct{ command, reply, site, want string }{
		"no device level":            {"site/{site_id}/cmd", "site/{site_id}/{device_id}/reply", "s", "{device_id}"},
		"device inside a level":      {"site/m-{device_id}/cmd", "site/{device_id}/reply", "s", "whole topic level"},
		"device twice":               {"{device_id}/{device_id}/cmd", "r/{device_id}", "s", "once"},
		"wildcard":                   {"site/+/{device_id}/cmd", "r/{device_id}", "s", "wildcard"},
		"unknown placeholder":        {"site/{gateway}/{device_id}/cmd", "r/{device_id}", "s", "{gateway}"},
		"same topics":                {"a/{device_id}", "a/{device_id}", "s", "differ"},
		"same rendered topics":       {"{site_id}/{device_id}", "site-01/{device_id}", "site-01", "differ"},
		"device-dependent collision": {"{device_id}/meter", "meter/{device_id}", "s", "differ"},
		"unsafe site":                {"site/{site_id}/{device_id}/cmd", "r/{site_id}/{device_id}", "a/b", "site"},
	} {
		if _, err := mqtt.NewTopics(c.command, c.reply, c.site); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want one mentioning %q", name, err, c.want)
		}
	}
}
