// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRedactorVariantsAndHeaderNames(t *testing.T) {
	t.Parallel()
	credential := `private'quoted"value\tail`
	for _, header := range []string{"Authorization", "Proxy-Authorization", "X-API-Key", "api-key", "Cookie", "X-MiXeD-ToKeN", "client_secret"} {
		value := credential
		if strings.Contains(strings.ToLower(header), "authorization") {
			value = "Bearer " + credential
		}
		r := newRedactor("", map[string]string{header: value, "X-Plain": "keep-me"})
		msg := fmt.Sprintf("raw=%s; quoted=%q; json=%s; plain=keep-me", credential, credential, jsonInner(credential))
		got := r.redact(msg)
		if want := `raw=***; quoted="***"; json=***; plain=keep-me`; got != want {
			t.Errorf("%s: redact() = %q, want %q", header, got, want)
		}
	}
	r := newRedactor("", map[string]string{"X-Plain": "keep-me"})
	if r.contains("keep-me") {
		t.Error("non-secret header value was treated as a credential")
	}
}

func TestRedactorLeavesCleanErrorsUntouched(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("network is unreachable")
	err := fmt.Errorf("dial tcp: %w", sentinel)
	r := newRedactor("ts_live_private")
	if got := r.redactError(err); got != err {
		t.Errorf("redactError changed a credential-free error: %v", got)
	}
	if got := (redactor{}).redactError(err); got != err {
		t.Errorf("zero redactor changed an error: %v", got)
	}
	got := r.redactError(fmt.Errorf("rejected ts_live_private: %w", sentinel))
	if got.Error() != "rejected ***: network is unreachable" {
		t.Errorf("redactError() = %q", got.Error())
	}
	if errors.Is(got, sentinel) || errors.Unwrap(got) != nil {
		t.Error("redacted error still wraps the original, which carries the credential")
	}
}

func TestNewClientAPIKeyWhitespaceIsStripped(t *testing.T) {
	var authorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"models": []}`))
	}))
	t.Cleanup(srv.Close)
	for _, source := range []string{"env", "option"} {
		for _, padding := range []string{"", "\n", "\r\n", " \t\r\n "} {
			key := padding + "test-key" + padding
			opts := []Option{WithBaseURL(srv.URL), WithHTTPClient(srv.Client())}
			if source == "env" {
				t.Setenv(APIKeyEnv, key)
			} else {
				t.Setenv(APIKeyEnv, "env-key")
				opts = append(opts, WithAPIKey(key))
			}
			client, err := NewClient(opts...)
			if err != nil {
				t.Fatalf("%s %q: NewClient: %v", source, padding, err)
			}
			if client.apiKey != "test-key" {
				t.Errorf("%s %q: apiKey = %q, want test-key", source, padding, client.apiKey)
			}
			if _, err := client.ListModels(t.Context()); err != nil {
				t.Fatalf("%s %q: ListModels: %v", source, padding, err)
			}
			if authorization != "Bearer test-key" {
				t.Errorf("%s %q: Authorization = %q", source, padding, authorization)
			}
		}
	}
}

func TestNewClientRejectsInvalidAPIKeyWithoutLeakingIt(t *testing.T) {
	const credential = "ts_live_private"
	for _, source := range []string{"env", "option"} {
		for _, character := range []string{"\n", "\r", "\t", "\x1f", "\x7f", " ", "\u00e9", "\u200b"} {
			key := credential + character + "suffix"
			var opts []Option
			if source == "env" {
				t.Setenv(APIKeyEnv, key)
			} else {
				t.Setenv(APIKeyEnv, "env-key")
				opts = append(opts, WithAPIKey(key))
			}
			_, err := NewClient(opts...)
			if err == nil {
				t.Fatalf("%s %q: NewClient accepted an invalid key", source, character)
			}
			if !strings.Contains(err.Error(), "api key") {
				t.Errorf("%s %q: error does not mention the api key: %v", source, character, err)
			}
			if strings.Contains(err.Error(), credential) {
				t.Errorf("%s %q: error leaks the api key: %v", source, character, err)
			}
		}
	}
}

func TestInvalidExplicitAPIKeyDoesNotFallBackToEnv(t *testing.T) {
	t.Setenv(APIKeyEnv, "env-key")
	for _, key := range []string{"", " \t\r\n ", "\x00private", "private\x00"} {
		_, err := NewClient(WithAPIKey(key))
		if err == nil {
			t.Errorf("WithAPIKey(%q) fell back to the environment", key)
		} else if strings.Contains(err.Error(), "private") {
			t.Errorf("WithAPIKey(%q) leaks the key: %v", key, err)
		}
	}
}

// echoTransport fails every request with an error built from its headers,
// mimicking transports that quote illegal header values in their messages.
type echoTransport struct {
	calls   int
	timeout bool
}

type echoError struct {
	msg     string
	timeout bool
}

func (e *echoError) Error() string { return e.msg }
func (e *echoError) Timeout() bool { return e.timeout }

func (tr *echoTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls++
	h := r.Header
	msg := fmt.Sprintf("illegal header value %q for Authorization; raw=%s; provider=%s; token=%q; proxy=%s; attempt %d",
		h.Get("Authorization"), strings.TrimPrefix(h.Get("Authorization"), "Bearer "), h.Get("X-Client-Secret"),
		h.Get("X-Proxy-Token"), h.Get("Proxy-Authorization"), tr.calls)
	return nil, &echoError{msg: msg, timeout: tr.timeout}
}

func TestConnectionErrorsDoNotExposeCredentials(t *testing.T) {
	t.Parallel()
	const (
		apiKey     = "ts_live_private"
		provider   = "provider-credential"
		proxyToken = `tok'en"quo\te`
		proxyBasic = "cHJveHk6c2VjcmV0"
	)
	quotedToken := strconv.Quote(proxyToken)
	secrets := []string{apiKey, provider, proxyToken, proxyBasic, quotedToken[1 : len(quotedToken)-1]}
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%v", timeout), func(t *testing.T) {
			t.Parallel()
			var log bytes.Buffer
			var retried []error
			policy := fastRetry(2)
			policy.OnRetry = func(_ int, err error, _ time.Duration) { retried = append(retried, err) }
			transport := &echoTransport{timeout: timeout}
			client, err := NewClient(
				WithAPIKey(apiKey),
				WithHTTPClient(&http.Client{Transport: transport}),
				WithHeaders(map[string]string{"X-Client-Secret": provider, "X-Proxy-Token": proxyToken}),
				WithRetryPolicy(policy),
				WithLogger(slog.New(slog.NewTextHandler(&log, nil))),
			)
			if err != nil {
				t.Fatal(err)
			}
			req := sampleRequest()
			req.Headers = map[string]string{"Proxy-Authorization": "Basic " + proxyBasic}
			_, err = client.Ask(t.Context(), req)
			if !errors.Is(err, ErrConnection) || errors.Is(err, ErrTimeout) != timeout {
				t.Fatalf("classification: got %v (timeout=%v)", err, errors.Is(err, ErrTimeout))
			}
			if transport.calls != 3 || len(retried) != 2 {
				t.Errorf("attempts = %d, retries = %d, want 3 and 2", transport.calls, len(retried))
			}
			want := `illegal header value "Bearer ***" for Authorization; raw=***; provider=***; token="***"; proxy=***; attempt 3`
			if !strings.HasSuffix(err.Error(), want) {
				t.Errorf("Error() = %q, want suffix %q", err.Error(), want)
			}
			texts := []string{err.Error(), log.String()}
			for _, r := range retried {
				texts = append(texts, r.Error())
			}
			for e := err; e != nil; e = errors.Unwrap(e) {
				texts = append(texts, e.Error())
			}
			for _, text := range texts {
				for _, secret := range secrets {
					if strings.Contains(text, secret) {
						t.Errorf("credential %q leaked: %q", secret, text)
					}
				}
			}
			if !strings.Contains(log.String(), "jev: retrying") {
				t.Errorf("retries were not logged: %q", log.String())
			}
		})
	}
}

func TestAskInvalidHeaderValueIsConnectionError(t *testing.T) {
	t.Parallel()
	var calls int
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	}, WithRetryPolicy(fastRetry(1)))
	req := sampleRequest()
	req.Headers = map[string]string{"X-Trace": "line\nbreak"}
	_, err := client.Ask(t.Context(), req)
	if !errors.Is(err, ErrConnection) || errors.Is(err, ErrTimeout) {
		t.Fatalf("invalid header value: got %v, want ErrConnection", err)
	}
	if calls != 0 {
		t.Errorf("request with an invalid header reached the server %d times", calls)
	}
	if strings.Contains(err.Error(), "sk-test-") {
		t.Errorf("error leaks the API key: %q", err.Error())
	}
	if !DefaultRetryPolicy().retryable(err) {
		t.Error("default policy does not retry a local protocol error")
	}
}
