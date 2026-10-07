package fallback

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type ModemManagerSender struct {
	modem  string
	runner CommandRunner
}

func NewModemManagerSender(modem string, runner CommandRunner) *ModemManagerSender {
	if runner == nil {
		runner = execRunner{}
	}
	return &ModemManagerSender{modem: modem, runner: runner}
}

func (s *ModemManagerSender) Send(ctx context.Context, destination, message string) error {
	if s.modem == "" || destination == "" || message == "" {
		return errors.New("modem, destination, and message are required")
	}
	if strings.ContainsAny(destination, ",=") || strings.Contains(message, ",") {
		return errors.New("destination or message contains an unsupported modem separator")
	}
	output, err := s.runner.Run(ctx, "mmcli", "-m", s.modem,
		"--messaging-create-sms=text="+message+",number="+destination)
	if err != nil {
		return fmt.Errorf("create modem SMS: %w: %s", err, strings.TrimSpace(string(output)))
	}
	path := smsPath(string(output))
	if path == "" {
		return fmt.Errorf("create modem SMS returned no SMS path: %s", strings.TrimSpace(string(output)))
	}
	output, err = s.runner.Run(ctx, "mmcli", "-s", path, "--send")
	if err != nil {
		return fmt.Errorf("send modem SMS: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func smsPath(output string) string {
	const marker = "/org/freedesktop/ModemManager1/SMS/"
	start := strings.Index(output, marker)
	if start < 0 {
		return ""
	}
	value := output[start:]
	if end := strings.IndexAny(value, " \t\r\n"); end >= 0 {
		value = value[:end]
	}
	return value
}
