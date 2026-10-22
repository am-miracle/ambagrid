package fallback

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

const DefaultSendTimeout = 30 * time.Second

type ModemManagerSender struct {
	modem       string
	runner      CommandRunner
	sendTimeout time.Duration
}

func NewModemManagerSender(modem string, runner CommandRunner, timeout time.Duration) *ModemManagerSender {
	if runner == nil {
		runner = execRunner{}
	}
	if timeout <= 0 {
		timeout = DefaultSendTimeout
	}
	return &ModemManagerSender{modem: modem, runner: runner, sendTimeout: timeout}
}

func (s *ModemManagerSender) Send(ctx context.Context, destination, message string) error {
	if s.modem == "" || destination == "" || message == "" {
		return errors.New("modem, destination, and message are required")
	}
	if strings.ContainsAny(destination, ",=") || strings.Contains(message, ",") {
		return errors.New("destination or message contains an unsupported modem separator")
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, s.sendTimeout)
	defer cancel()
	output, err := s.runner.Run(ctx, "mmcli", "-m", s.modem,
		"--messaging-create-sms=text="+message+",number="+destination)
	if err != nil {
		return fmt.Errorf("create modem SMS: %w: %s", err, redactOutput(strings.TrimSpace(string(output))))
	}
	path := smsPath(string(output))
	if path == "" {
		return fmt.Errorf("create modem SMS returned no SMS path: %s", redactOutput(strings.TrimSpace(string(output))))
	}
	output, err = s.runner.Run(ctx, "mmcli", "-s", path, "--send")
	if err != nil {
		return fmt.Errorf("send modem SMS: %w: %s", err, redactOutput(strings.TrimSpace(string(output))))
	}
	return nil
}

var phonePattern = regexp.MustCompile(`\+?\d[\d\s\-]{6,}\d`)

func redactOutput(output string) string {
	return phonePattern.ReplaceAllStringFunc(output, func(match string) string {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, match)
		if len(digits) < 7 {
			return match
		}
		return digits[:3] + "***" + digits[len(digits)-2:]
	})
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
