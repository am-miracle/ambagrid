package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"edge-agent-go/internal/domain"
)

// ErrReportRefused marks a report the platform will never accept, such as one
// for an unknown command or one contradicting a recorded outcome.
var ErrReportRefused = errors.New("platform refused the command report")

const maxResponseBytes = 1 << 20

// Keep in sync with api-go/internal/domain and the fake platform in runner_test.go;
// exceeding this limit abandons the report and leaves the platform at sent.
const maxFailureReasonBytes = 500

type Pending struct {
	CommandID   string    `json:"command_id"`
	MeterID     string    `json:"meter_id"`
	DeviceID    string    `json:"device_id"`
	CommandType string    `json:"command_type"`
	Status      string    `json:"status"`
	RequestedAt time.Time `json:"requested_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	ExpiresInMS *int64    `json:"expires_in_ms"`
	CreditKWh   float64   `json:"credit_kwh,omitempty"`
	// Deadline uses ExpiresInMS on the monotonic clock when supplied, so wall
	// clock errors or jumps cannot expire a live command or accept a late one.
	Deadline time.Time `json:"-"`
}

type HTTPClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	// clockOffset is the platform clock minus this gateway's, in nanoseconds.
	clockOffset atomic.Int64
}

const (
	// The Date header has one-second resolution; smaller offsets are noise.
	minClockOffset = 2 * time.Second
	// Beyond this round trip the midpoint says too little about when the
	// platform stamped its Date header.
	maxOffsetRoundTrip = 5 * time.Second
)

func NewHTTPClient(baseURL, apiKey string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *HTTPClient) Poll(ctx context.Context) ([]Pending, error) {
	var body struct {
		Data []Pending `json:"data"`
	}
	// Taken before sending, so each deadline errs early by the round trip.
	sent := time.Now()
	header, err := c.post(ctx, c.baseURL+"/v1/edge/commands/poll", nil, &body)
	if err != nil {
		return nil, fmt.Errorf("poll commands: %w", err)
	}
	c.observeClock(sent, time.Now(), header.Get("Date"))
	for i := range body.Data {
		p := &body.Data[i]
		if p.ExpiresInMS != nil {
			p.Deadline = sent.Add(time.Duration(*p.ExpiresInMS) * time.Millisecond)
		} else {
			p.Deadline = p.ExpiresAt
		}
	}
	return body.Data, nil
}

// observeClock estimates the platform clock offset from its Date header,
// using the request midpoint to account for round-trip time.
func (c *HTTPClient) observeClock(sent, received time.Time, date string) {
	server, err := http.ParseTime(date)
	roundTrip := received.Sub(sent)
	if err != nil || roundTrip > maxOffsetRoundTrip {
		return
	}
	offset := server.Sub(sent.Add(roundTrip / 2))
	if offset.Abs() < minClockOffset {
		offset = 0
	}
	c.clockOffset.Store(int64(offset))
}

// ClockOffset is the latest estimate of the platform clock minus this gateway's.
func (c *HTTPClient) ClockOffset() time.Duration {
	return time.Duration(c.clockOffset.Load())
}

func (c *HTTPClient) Report(ctx context.Context, report domain.CommandReport) error {
	payload, err := json.Marshal(struct {
		Status        domain.CommandReportStatus `json:"status"`
		OccurredAt    time.Time                  `json:"occurred_at"`
		FailureReason string                     `json:"failure_reason,omitempty"`
	}{report.Status, report.OccurredAt.Add(c.ClockOffset()).UTC(), wireFailureReason(report)})
	if err != nil {
		return fmt.Errorf("encode command report: %w", err)
	}
	endpoint := c.baseURL + "/v1/edge/commands/" + url.PathEscape(report.CommandID) + "/report"
	if _, err := c.post(ctx, endpoint, payload, nil); err != nil {
		return fmt.Errorf("report command %q %s: %w", report.CommandID, report.Status, err)
	}
	return nil
}

// wireFailureReason bounds only what is sent; the ledger keeps the device's
// full message. It is deterministic so retries carry identical evidence.
func wireFailureReason(report domain.CommandReport) string {
	if report.Status != domain.ReportFailed {
		return report.FailureReason
	}
	// JSON encoding would replace each invalid byte with a 3-byte U+FFFD.
	reason := strings.ToValidUTF8(report.FailureReason, "\uFFFD")
	if strings.TrimSpace(reason) == "" {
		return "rejected without a reason"
	}
	if len(reason) <= maxFailureReasonBytes {
		return reason
	}
	const marker = "…"
	cut := maxFailureReasonBytes - len(marker)
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut] + marker
}

func (c *HTTPClient) post(ctx context.Context, endpoint string, payload []byte, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil {
			if err := json.Unmarshal(body, out); err != nil {
				return nil, fmt.Errorf("decode response: %w", err)
			}
		}
		return resp.Header, nil
	// 401 and 403 stay retryable: a wrong key is fixed by an operator, after
	// which the queued reports must still be delivered.
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusConflict:
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrReportRefused, resp.StatusCode, bytes.TrimSpace(body))
	default:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
}
