// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestRetryPolicyDelay(t *testing.T) {
	t.Parallel()
	p := RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	for _, tt := range []struct {
		attempt    int
		retryAfter time.Duration
		want       time.Duration
	}{
		{1, 0, 100 * time.Millisecond},
		{2, 0, 200 * time.Millisecond},
		{3, 0, 400 * time.Millisecond},
		{4, 0, 800 * time.Millisecond},
		{5, 0, time.Second},
		{50, 0, time.Second},
		{1, 3 * time.Second, 3 * time.Second},
		{1, 10 * time.Millisecond, 10 * time.Millisecond},
	} {
		if got := p.delay(tt.attempt, tt.retryAfter); got != tt.want {
			t.Errorf("delay(%d, %v): got = %v, want = %v", tt.attempt, tt.retryAfter, got, tt.want)
		}
	}
	ignoring := p
	ignoring.IgnoreRetryAfter = true
	if got := ignoring.delay(1, 3*time.Second); got != 100*time.Millisecond {
		t.Errorf("IgnoreRetryAfter: got = %v, want = 100ms", got)
	}
	if got := (RetryPolicy{}).delay(1, 0); got != 0 {
		t.Errorf("zero policy delay: got = %v, want = 0", got)
	}
	jittered := p
	jittered.Jitter = 0.25
	for range 50 {
		if got := jittered.delay(1, 0); got > 100*time.Millisecond || got < 75*time.Millisecond {
			t.Fatalf("jittered delay %v outside [75ms, 100ms]", got)
		}
	}
}

func TestRetryPolicyRetryable(t *testing.T) {
	t.Parallel()
	def := DefaultRetryPolicy()
	custom := def
	custom.Statuses = []int{http.StatusBadGateway}
	custom.ConnectionErrors = false
	custom.Timeouts = false
	custom.Predicate = func(err error) bool { return errors.Is(err, ErrResponseTooLarge) }
	for _, tt := range []struct {
		name   string
		policy RetryPolicy
		err    error
		want   bool
	}{
		{"nil", def, nil, false},
		{"429", def, &APIError{StatusCode: 429}, true},
		{"408", def, &APIError{StatusCode: 408}, true},
		{"529", def, &APIError{StatusCode: StatusOverloaded}, true},
		{"401", def, &APIError{StatusCode: 401}, false},
		{"422", def, &APIError{StatusCode: 422}, false},
		{"connection", def, &ConnectionError{Err: errors.New("refused")}, true},
		{"timeout", def, &ConnectionError{Timeout: true, Err: errors.New("timeout")}, true},
		{"validation", def, validationError("bad"), false},
		{"custom statuses include", custom, &APIError{StatusCode: 502}, true},
		{"custom statuses exclude 429", custom, &APIError{StatusCode: 429}, false},
		{"custom no connection", custom, &ConnectionError{Err: errors.New("refused")}, false},
		{"custom no timeout", custom, &ConnectionError{Timeout: true, Err: errors.New("t")}, false},
		{"custom predicate", custom, ErrResponseTooLarge, true},
	} {
		if got := tt.policy.retryable(tt.err); got != tt.want {
			t.Errorf("%s: retryable = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		h    http.Header
		want time.Duration
	}{
		{"none", http.Header{}, 0},
		{"seconds", http.Header{"Retry-After": {"3"}}, 3 * time.Second},
		{"fractional seconds", http.Header{"Retry-After": {"0.5"}}, 500 * time.Millisecond},
		{"negative", http.Header{"Retry-After": {"-1"}}, 0},
		{"garbage", http.Header{"Retry-After": {"soon"}}, 0},
		{"http date", http.Header{"Retry-After": {now.Add(90 * time.Second).Format(http.TimeFormat)}}, 90 * time.Second},
		{"past http date", http.Header{"Retry-After": {now.Add(-time.Minute).Format(http.TimeFormat)}}, 0},
		{"ms wins", http.Header{"Retry-After": {"3"}, "Retry-After-Ms": {"250"}}, 250 * time.Millisecond},
		{"ms garbage falls back", http.Header{"Retry-After": {"2"}, "Retry-After-Ms": {"x"}}, 2 * time.Second},
	} {
		if got := parseRetryAfter(tt.h, now); got != tt.want {
			t.Errorf("%s: got = %v, want = %v", tt.name, got, tt.want)
		}
	}
}

func TestExtractMessage(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		body string
		want string
	}{
		{`{"error":"nope"}`, "nope"},
		{`{"error":{"message":"nested"}}`, "nested"},
		{`{"message":"plain"}`, "plain"},
		{`{"detail":"detail text"}`, "detail text"},
		{`{"detail":{"message":"detail nested"}}`, "detail nested"},
		{`{"detail":[{"loc":["body","state"],"msg":"Field required"},{"loc":["body"],"msg":"too big"}]}`, "state: Field required; too big"},
		{`"just a string"`, "just a string"},
		{`{"unknown":1}`, `{"unknown":1}`},
		{"plain text\x00with control", "plain text with control"},
		{"", ""},
	} {
		if got := extractMessage([]byte(tt.body)); got != tt.want {
			t.Errorf("extractMessage(%q): got = %q, want = %q", tt.body, got, tt.want)
		}
	}
}
