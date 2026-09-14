// parses and validates HTTP query parameters.
package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errInvalidParameter = errors.New("invalid request parameter")

// limitParam returns zero when the client omits the page size.
func limitParam(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: limit must be an integer", errInvalidParameter)
	}
	if limit < 1 {
		return 0, fmt.Errorf("%w: limit must be greater than zero", errInvalidParameter)
	}
	return limit, nil
}

func cursorParam(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("cursor"))
}

// optionalParam returns nil for an absent or blank value.
func optionalParam(r *http.Request, key string) *string {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return nil
	}
	return &value
}

func enumParam[T any](r *http.Request, key string, parse func(string) (T, error)) (*T, error) {
	raw := optionalParam(r, key)
	if raw == nil {
		return nil, nil
	}

	value, err := parse(*raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errInvalidParameter, err)
	}
	return &value, nil
}

func requiredEnumParam[T any](r *http.Request, key string, parse func(string) (T, error)) (T, error) {
	var zero T
	raw := optionalParam(r, key)
	if raw == nil {
		return zero, fmt.Errorf("%w: %s is required", errInvalidParameter, key)
	}
	value, err := parse(*raw)
	if err != nil {
		return zero, fmt.Errorf("%w: %s", errInvalidParameter, err)
	}
	return value, nil
}

func requiredTimeParam(r *http.Request, key string) (time.Time, error) {
	raw := optionalParam(r, key)
	if raw == nil {
		return time.Time{}, fmt.Errorf("%w: %s is required", errInvalidParameter, key)
	}
	value, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s must be an RFC 3339 timestamp", errInvalidParameter, key)
	}
	return value, nil
}
