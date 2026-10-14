package fallback_test

import (
	"context"
	"reflect"
	"testing"

	"edge-agent-go/internal/fallback"
)

type commandCall struct {
	name string
	args []string
}

type fakeRunner struct {
	calls []commandCall
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, commandCall{name: name, args: args})
	if len(r.calls) == 1 {
		return []byte("Successfully created new SMS: /org/freedesktop/ModemManager1/SMS/12"), nil
	}
	return []byte("successfully sent the SMS"), nil
}

func TestModemManagerSenderCreatesAndSendsSMS(t *testing.T) {
	runner := &fakeRunner{}
	sender := fallback.NewModemManagerSender("0", runner)

	if err := sender.Send(context.Background(), "+2348000000000", "AMBAGRID|v=1"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	want := []commandCall{
		{name: "mmcli", args: []string{"-m", "0", "--messaging-create-sms=text=AMBAGRID|v=1,number=+2348000000000"}},
		{name: "mmcli", args: []string{"-s", "/org/freedesktop/ModemManager1/SMS/12", "--send"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}
