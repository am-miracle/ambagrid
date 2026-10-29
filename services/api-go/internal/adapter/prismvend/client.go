// Package prismvend implements the PrismVend v5.22.2 VendCredit2 HTTP contract.
package prismvend

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"api-go/internal/domain"
)

type Client struct {
	endpoint string
	http     *http.Client
}

func New(baseURL string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("PrismVend base URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	return &Client{endpoint: strings.TrimRight(baseURL, "/") + "/stsvend/VendCredit2.ini", http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Vend(ctx context.Context, request domain.STSVendRequest) (domain.STSVend, error) {
	if err := request.Validate(); err != nil {
		return domain.STSVend{}, err
	}
	values := url.Values{"meterId": {request.MeterID}, "subclass": {"0"}, "value": {strconv.FormatInt(request.AmountMinor, 10)}, "messageId": {request.Reference}, "vendType": {"1"}}
	var obtainedConnection atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { obtainedConnection.Store(true) }})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return domain.STSVend{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.http.Do(req)
	if err != nil {
		// Only a failure before obtaining a connection proves that no POST could
		// have been sent. Any error after that remains conservatively uncertain.
		if !obtainedConnection.Load() {
			return domain.STSVend{}, fmt.Errorf("%w: %w", domain.ErrVendNotSent, err)
		}
		return domain.STSVend{}, fmt.Errorf("PrismVend transport: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return domain.STSVend{}, fmt.Errorf("PrismVend returned HTTP %d", response.StatusCode)
	}
	const maxBody = 64 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return domain.STSVend{}, err
	}
	if len(body) > maxBody {
		return domain.STSVend{}, fmt.Errorf("PrismVend response too large")
	}
	fields := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return domain.STSVend{}, fmt.Errorf("malformed PrismVend INI")
		}
		if _, duplicate := fields[key]; duplicate {
			return domain.STSVend{}, fmt.Errorf("duplicate PrismVend field")
		}
		fields[key] = value
	}
	if err := scanner.Err(); err != nil {
		return domain.STSVend{}, err
	}
	count, err := strconv.Atoi(fields["numTokens"])
	if err != nil || count < 1 || count > 16 {
		return domain.STSVend{}, fmt.Errorf("invalid PrismVend token count")
	}
	result := domain.STSVend{UnitName: fields["unitName"], IDRecord: fields["idRecord"], ToIDRecord: fields["toIdRecord"]}
	for i := 1; i <= count; i++ {
		token := fields[fmt.Sprintf("tokenDec_%d", i)]
		result.Tokens = append(result.Tokens, token)
	}
	result.Units, err = strconv.ParseFloat(fields["unitsActual"], 64)
	if err != nil {
		return domain.STSVend{}, fmt.Errorf("invalid PrismVend units")
	}
	seconds, err := strconv.ParseInt(fields["vendTimeUnix"], 10, 64)
	if err != nil || seconds <= 0 {
		return domain.STSVend{}, fmt.Errorf("invalid PrismVend vend time")
	}
	result.VendedAt = time.Unix(seconds, 0).UTC()
	if err := result.Validate(); err != nil {
		return domain.STSVend{}, fmt.Errorf("validate PrismVend result: %w", err)
	}
	return result, nil
}
