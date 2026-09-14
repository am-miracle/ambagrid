// provides tracing, logging, recovery, timeout, and CORS middleware.
package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
)

type middleware func(http.Handler) http.Handler

type contextKey int

const requestIDKey contextKey = iota

const requestIDHeader = "X-Request-Id"
const operatorIDHeader = "X-Operator-Id"

// RequestIDFrom returns the request's correlation ID, if present.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get(requestIDHeader))
		if id == "" {
			id = newRequestID()
		}

		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// sanitizeRequestID accepts only values safe for structured logs.
func sanitizeRequestID(value string) string {
	const maxLength = 64

	if len(value) == 0 || len(value) > maxLength {
		return ""
	}
	for _, char := range value {
		isAllowed := (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9')
		if !isAllowed {
			return ""
		}
	}
	return value
}

func newRequestID() string {
	var buf [16]byte
	// rand.Read is documented never to fail on any supported platform.
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func logRequests(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			// Do not log query parameters because they can contain customer IDs.
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration_ms", time.Since(started).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.wroteHeader {
		s.status = status
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// recoverPanics converts unexpected panics into correlated 500 responses.
func recoverPanics(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				// Let net/http handle its connection-abort sentinel.
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				logger.Error("panic serving request",
					"panic", recovered,
					"path", r.URL.Path,
					"request_id", RequestIDFrom(r.Context()),
				)
				writeErrorBody(w, logger, http.StatusInternalServerError, codeInternal, "internal error", RequestIDFrom(r.Context()))
			}()

			next.ServeHTTP(w, r)
		})
	}
}

func withTimeout(timeout time.Duration) middleware {
	return func(next http.Handler) http.Handler {
		if timeout <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// withCORS permits browser requests from configured control-room origins.
func withCORS(allowedOrigins []string) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && slices.Contains(allowedOrigins, origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", strings.Join([]string{"Content-Type", requestIDHeader, operatorIDHeader}, ", "))
				w.Header().Set("Access-Control-Max-Age", "600")
				// Tell shared caches that the response varies by origin.
				w.Header().Add("Vary", "Origin")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
