// Tests HTTP middleware behavior and composition.
package controller

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDIsEchoedAndGeneratedWhenAbsent(t *testing.T) {
	api := newTestAPI()

	recorder := api.get(t, "/healthz")

	requestID := recorder.Header().Get(requestIDHeader)
	if requestID == "" {
		t.Fatal("no request ID on the response")
	}
	if bodyID := decodeBody(t, recorder)["request_id"]; bodyID != requestID {
		t.Fatalf("body request_id = %v, header = %q", bodyID, requestID)
	}
}

func TestAnInboundRequestIDIsReusedOnlyWhenItIsSafeToLog(t *testing.T) {
	tests := map[string]struct {
		inbound string
		reused  bool
	}{
		"plain token":            {inbound: "trace123abc", reused: true},
		"hyphen":                 {inbound: "trace-123", reused: false},
		"underscore":             {inbound: "trace_123", reused: false},
		"newline injection":      {inbound: "abc\ndef", reused: false},
		"quotes and spaces":      {inbound: `a" b`, reused: false},
		"longer than the header": {inbound: string(make([]byte, 65)), reused: false},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			api := newTestAPI()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			request.Header.Set(requestIDHeader, test.inbound)

			api.handler.ServeHTTP(recorder, request)

			got := recorder.Header().Get(requestIDHeader)
			if test.reused && got != test.inbound {
				t.Fatalf("request ID = %q, want the inbound %q", got, test.inbound)
			}
			if !test.reused && got == test.inbound {
				t.Fatalf("request ID %q was reused unsanitized", got)
			}
			if got == "" {
				t.Fatal("no request ID on the response")
			}
		})
	}
}

func TestCORSAnswersOnlyAllowedOrigins(t *testing.T) {
	tests := map[string]struct {
		origin string
		allow  bool
	}{
		"the control room": {origin: "http://localhost:5173", allow: true},
		"anywhere else":    {origin: "https://evil.example", allow: false},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			api := newTestAPI()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodOptions, "/v1/alerts", nil)
			request.Header.Set("Origin", test.origin)

			api.handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNoContent {
				t.Fatalf("preflight status = %d, want 204", recorder.Code)
			}
			got := recorder.Header().Get("Access-Control-Allow-Origin")
			if test.allow && got != test.origin {
				t.Fatalf("Allow-Origin = %q, want %q", got, test.origin)
			}
			if !test.allow && got != "" {
				t.Fatalf("Allow-Origin = %q, want none", got)
			}
		})
	}
}

func TestAPanicBecomesA500RatherThanADroppedConnection(t *testing.T) {
	handler := withRequestID(
		recoverPanics(slog.New(slog.NewTextHandler(io.Discard, nil)))(
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("nil map read")
			}),
		),
	)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/alerts", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	detail := decodeBody(t, recorder)["error"].(map[string]any)
	if detail["request_id"] == "" || detail["request_id"] != recorder.Header().Get(requestIDHeader) {
		t.Fatalf("panic request_id = %v, header = %q", detail["request_id"], recorder.Header().Get(requestIDHeader))
	}
}
