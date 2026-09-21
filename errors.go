// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	// ErrInvalidRequest identifies a request rejected before it was sent.
	ErrInvalidRequest = errors.New("jev: invalid request")
	// ErrResponseValidation identifies an HTTP success whose body does not
	// satisfy the API contract or the questions as sent.
	ErrResponseValidation = errors.New("jev: response failed validation")
	// ErrResponseTooLarge identifies a response body above the client's cap.
	ErrResponseTooLarge = errors.New("jev: response exceeds size cap")
	// ErrNoAnswer identifies a typed getter called for a question id that
	// has no answer of that kind.
	ErrNoAnswer = errors.New("jev: no answer of the requested kind")

	// ErrConnection identifies a request that failed without an HTTP
	// response. A [*ConnectionError] matches it.
	ErrConnection = errors.New("jev: connection error")
	// ErrTimeout identifies a request that exceeded its HTTP timeout. A
	// [*ConnectionError] with Timeout set matches it, and ErrConnection.
	ErrTimeout = errors.New("jev: request timed out")

	// Status classes. An [*APIError] matches the one for its status through
	// errors.Is, so callers can branch without comparing numbers.
	ErrBadRequest    = errors.New("jev: bad request")           // 400
	ErrUnauthorized  = errors.New("jev: authentication failed") // 401
	ErrForbidden     = errors.New("jev: permission denied")     // 403
	ErrNotFound      = errors.New("jev: not found")             // 404
	ErrUnprocessable = errors.New("jev: request failed server validation")
	ErrRateLimited   = errors.New("jev: rate limit exceeded") // 429
	ErrServer        = errors.New("jev: server error")        // 5xx, including 529
)

// StatusOverloaded is the non-standard status the API returns while
// temporarily overloaded; net/http has no constant for it.
const StatusOverloaded = 529

// RequestIDHeader is the response header carrying the server's request id.
const RequestIDHeader = "X-Typesafe-Request-Id"

// APIError is a non-2xx response from the API.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Message is the server's explanation, extracted from the JSON body's
	// "error", "message", or "detail" fields (including FastAPI-style
	// validation detail lists), or a bounded excerpt of the body otherwise.
	Message string
	// Body is the raw response body, for callers that need more than Message.
	Body []byte
	// Endpoint is the request method and path, without credentials or query.
	Endpoint string
	// RequestID is the server's request id, or "" when absent.
	RequestID string
	// RetryAfter is the server's Retry-After or retry-after-ms hint, or zero
	// when absent.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("jev: ")
	if e.Endpoint != "" {
		b.WriteString(e.Endpoint)
		b.WriteString(": ")
	}
	b.WriteString("HTTP ")
	b.WriteString(strconv.Itoa(e.StatusCode))
	if text := statusText(e.StatusCode); text != "" {
		b.WriteString(" " + text)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.RequestID != "" {
		b.WriteString(" (request_id=" + e.RequestID + ")")
	}
	return b.String()
}

// Is reports whether target is the status-class sentinel for e's status.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrBadRequest:
		return e.StatusCode == http.StatusBadRequest
	case ErrUnauthorized:
		return e.StatusCode == http.StatusUnauthorized
	case ErrForbidden:
		return e.StatusCode == http.StatusForbidden
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrUnprocessable:
		return e.StatusCode == http.StatusUnprocessableEntity
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	case ErrServer:
		return e.StatusCode >= http.StatusInternalServerError
	}
	return false
}

func statusText(code int) string {
	if code == StatusOverloaded {
		return "Overloaded"
	}
	return http.StatusText(code)
}

// ConnectionError is a request that failed without an HTTP response: the
// server could not be reached, the connection dropped, or the HTTP timeout
// elapsed. It matches [ErrConnection], and [ErrTimeout] when Timeout is set.
type ConnectionError struct {
	// Endpoint is the request method and path.
	Endpoint string
	// Timeout reports whether the failure was a timeout.
	Timeout bool
	// Err is the underlying transport error.
	Err error
}

func (e *ConnectionError) Error() string {
	kind := "connection error"
	if e.Timeout {
		kind = "request timed out"
	}
	return fmt.Sprintf("jev: %s: %s: %v", e.Endpoint, kind, e.Err)
}

// Unwrap returns the transport error.
func (e *ConnectionError) Unwrap() error { return e.Err }

// Is reports whether target is ErrConnection, or ErrTimeout for a timeout.
func (e *ConnectionError) Is(target error) bool {
	return target == ErrConnection || (target == ErrTimeout && e.Timeout)
}

// newConnectionError classifies a transport error from http.Client.Do.
// net/http reports its own timeouts as errors that also match
// context.DeadlineExceeded, so the Timeout method decides; the caller
// separately checks whether its own context expired. If the transport echoed
// a credential into its message, Err is replaced by a flat error carrying
// only the masked text; otherwise it is kept as-is for errors.Is/As.
func newConnectionError(endpoint string, err error, redact redactor) *ConnectionError {
	timeout := false
	if netErr, ok := errors.AsType[net.Error](err); ok {
		timeout = netErr.Timeout()
	}
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		timeout = urlErr.Timeout()
		err = urlErr.Err
	}
	return &ConnectionError{Endpoint: endpoint, Timeout: timeout, Err: redact.redactError(err)}
}

const messageExcerptLimit = 200

// extractMessage pulls a human-readable explanation from an error body,
// mirroring the official SDKs: "error" (string or {message}), "message",
// "detail" (string, {message}, or a list of {loc, msg} validation entries),
// falling back to a bounded, control-character-free excerpt of the body.
func extractMessage(body []byte) string {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return excerpt(string(body))
	}
	switch v := decoded.(type) {
	case string:
		return excerpt(v)
	case map[string]any:
		if s, ok := v["error"].(string); ok && s != "" {
			return s
		}
		if m, ok := v["error"].(map[string]any); ok {
			if s, ok := m["message"].(string); ok && s != "" {
				return s
			}
		}
		if s, ok := v["message"].(string); ok && s != "" {
			return s
		}
		switch detail := v["detail"].(type) {
		case string:
			if detail != "" {
				return detail
			}
		case map[string]any:
			if s, ok := detail["message"].(string); ok && s != "" {
				return s
			}
		case []any:
			if s := validationDetail(detail); s != "" {
				return s
			}
		}
	}
	return excerpt(string(body))
}

// validationDetail renders FastAPI-style validation entries as
// "path: message; path: message", dropping the leading "body" location.
func validationDetail(entries []any) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := m["msg"].(string)
		if !ok {
			continue
		}
		var path []string
		if loc, ok := m["loc"].([]any); ok {
			for _, item := range loc {
				if s := fmt.Sprint(item); s != "body" {
					path = append(path, s)
				}
			}
		}
		if len(path) > 0 {
			msg = strings.Join(path, ".") + ": " + msg
		}
		parts = append(parts, msg)
	}
	return strings.Join(parts, "; ")
}

// excerpt renders untrusted text for an error message: control and format
// runes become spaces and the result is capped.
func excerpt(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > messageExcerptLimit {
		s = s[:messageExcerptLimit] + "..."
	}
	return s
}

// parseRetryAfter reads the server's wait hint: retry-after-ms in
// milliseconds first, then Retry-After as seconds or as an HTTP date. A
// missing or unparsable value yields zero.
func parseRetryAfter(header http.Header, now time.Time) time.Duration {
	if raw := strings.TrimSpace(header.Get("Retry-After-Ms")); raw != "" {
		if ms, err := strconv.ParseFloat(raw, 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds * float64(time.Second))
	}
	if at, err := http.ParseTime(raw); err == nil {
		return max(at.Sub(now), 0)
	}
	return 0
}

// isContextError reports whether err is the caller's own context ending.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
