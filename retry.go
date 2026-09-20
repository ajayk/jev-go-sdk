// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"crypto/rand"
	"errors"
	"math/big"
	"slices"
	"time"
)

// RetryPolicy bounds the retries the client makes on transient failures. It
// mirrors the official SDKs' policy. Start from [DefaultRetryPolicy] and
// adjust fields; a zero RetryPolicy retries nothing.
type RetryPolicy struct {
	// MaxRetries is the number of additional attempts after the first. Zero
	// disables retries.
	MaxRetries int
	// BaseDelay is the first backoff delay, doubled on each retry up to
	// MaxDelay. Zero disables backoff.
	BaseDelay time.Duration
	// MaxDelay caps the backoff delay. Zero disables backoff.
	MaxDelay time.Duration
	// Jitter is the fraction of each backoff delay, in [0, 1], randomly
	// subtracted to spread retries out.
	Jitter float64
	// Statuses lists the HTTP statuses that are retried. Nil means the
	// default set: 408, 429, and every 5xx (which includes 529).
	Statuses []int
	// IgnoreRetryAfter disables honouring the server's Retry-After and
	// retry-after-ms headers, which otherwise replace the computed backoff.
	IgnoreRetryAfter bool
	// ConnectionErrors retries requests that failed without an HTTP
	// response (connection refused, reset, DNS failure).
	ConnectionErrors bool
	// Timeouts retries requests that exceeded the HTTP timeout.
	Timeouts bool
	// Predicate, if set, is consulted after the built-in rules and can mark
	// additional errors as retryable.
	Predicate func(error) bool
	// Budget bounds the total time spent on one call, including the first
	// attempt and all delays. A retry whose delay would reach the budget is
	// not made and the last error is returned. Zero means no budget.
	Budget time.Duration
	// OnRetry, if set, is called before each retry with the attempt number
	// (starting at 1), the error being retried, and the delay to be slept.
	OnRetry func(attempt int, err error, delay time.Duration)
}

// DefaultRetryPolicy matches the official SDKs: two retries starting at
// 500 ms with a 5 s cap and 25% jitter, on 408, 429, 5xx, connection errors,
// and timeouts, honouring Retry-After, within a 30 s budget.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:       2,
		BaseDelay:        500 * time.Millisecond,
		MaxDelay:         5 * time.Second,
		Jitter:           0.25,
		ConnectionErrors: true,
		Timeouts:         true,
		Budget:           30 * time.Second,
	}
}

func (p RetryPolicy) validate() error {
	if p.MaxRetries < 0 || p.BaseDelay < 0 || p.MaxDelay < 0 || p.Budget < 0 {
		return errors.New("retry policy durations and counts must not be negative")
	}
	if p.Jitter < 0 || p.Jitter > 1 {
		return errors.New("retry policy jitter must be between 0 and 1")
	}
	return nil
}

// retryable reports whether err qualifies for another attempt.
func (p RetryPolicy) retryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	var connErr *ConnectionError
	builtin := false
	switch {
	case errors.As(err, &apiErr):
		statuses := p.Statuses
		if statuses == nil {
			builtin = apiErr.StatusCode == 408 || apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
		} else {
			builtin = slices.Contains(statuses, apiErr.StatusCode)
		}
	case errors.As(err, &connErr):
		if connErr.Timeout {
			builtin = p.Timeouts
		} else {
			builtin = p.ConnectionErrors
		}
	}
	return builtin || (p.Predicate != nil && p.Predicate(err))
}

// delay returns the wait before retry number attempt (starting at 1): the
// server's hint when present and honoured, otherwise exponential backoff
// with subtractive jitter.
func (p RetryPolicy) delay(attempt int, retryAfter time.Duration) time.Duration {
	if !p.IgnoreRetryAfter && retryAfter > 0 {
		return retryAfter
	}
	if p.BaseDelay == 0 || p.MaxDelay == 0 {
		return 0
	}
	d := p.BaseDelay
	for range attempt - 1 {
		if d >= p.MaxDelay/2 {
			d = p.MaxDelay
			break
		}
		d *= 2
	}
	d = min(d, p.MaxDelay)
	if p.Jitter > 0 {
		if n, err := rand.Int(rand.Reader, big.NewInt(1<<30)); err == nil {
			fraction := float64(n.Int64()) / float64(1<<30)
			d -= time.Duration(float64(d) * fraction * p.Jitter)
		}
	}
	return d
}
