// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"context"
	"errors"
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
)

// StatusOverloaded is the non-standard status the API returns while
// temporarily overloaded; net/http has no constant for it.
const StatusOverloaded = 529

// APIError is a non-2xx response from the API.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Body is a bounded, control-character-free excerpt of the response body.
	Body string
	// RetryAfter is the server's Retry-After hint, or zero when absent.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	msg := "jev: HTTP " + strconv.Itoa(e.StatusCode)
	if text := statusText(e.StatusCode); text != "" {
		msg += " " + text
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Retryable reports whether the status is one the API documents as
// transient: rate limiting, overload, request timeout, and server errors.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode == http.StatusRequestTimeout ||
		e.StatusCode >= http.StatusInternalServerError
}

func statusText(code int) string {
	if code == StatusOverloaded {
		return "Overloaded"
	}
	return http.StatusText(code)
}

// IsRetryable reports whether err is a transient failure worth another
// attempt: a retryable [APIError], an HTTP client or transport timeout, or
// another network timeout. Validation failures and client errors are never
// retryable, nor is a bare context error. net/http reports its own timeouts
// as errors that also match context.DeadlineExceeded, so a transport error
// is classified by its Timeout method before the context sentinels are
// consulted; [Client.Ask] separately stops retrying once the caller's
// context is done.
func IsRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Timeout()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

const bodyExcerptLimit = 512

// bodyExcerpt renders an error body for a log-safe message: control and
// format runes become spaces and the result is capped.
func bodyExcerpt(body []byte) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, string(body))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > bodyExcerptLimit {
		s = s[:bodyExcerptLimit] + "..."
	}
	return s
}

// parseRetryAfter reads a Retry-After header given in seconds. HTTP-date
// forms and unparsable values yield zero.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
