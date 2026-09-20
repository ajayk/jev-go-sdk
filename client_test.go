// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetry(maxRetries int) RetryPolicy {
	p := DefaultRetryPolicy()
	p.MaxRetries = maxRetries
	p.BaseDelay, p.MaxDelay, p.Jitter = time.Millisecond, time.Millisecond, 0
	return p
}

func sampleRequest() Request {
	return Request{
		State: "The install script downloads and executes a remote binary.",
		Questions: map[string]Question{
			"exfil": Noul{Instructions: "Does the state describe data exfiltration?"},
			"kind": Choice{
				Instructions: "Classify the behavior.",
				Options:      map[string]Content{"benign": nil, "suspicious": "warrants review", "malicious": "clearly hostile"},
			},
			"severity": Score{Instructions: "Rate the severity.", Levels: []Content{"none", "low", "high"}},
		},
	}
}

const sampleAnswers = `{
	"exfil": {"type": "noul", "noul": 0.91},
	"kind": {"type": "choice", "choice": "malicious", "probabilities": {"benign": 0.02, "suspicious": 0.18, "malicious": 0.80}, "confidence": 0.77},
	"severity": {"type": "score", "score": 1.6, "legend": {"0": "none", "1": "low", "2": "high"}, "probabilities": {"0": 0.1, "1": 0.2, "2": 0.7}, "confidence": 0.65}
}`

func sampleBody(answers string) string {
	return `{"model": "jev-1.13.0", "answers": ` + answers + `, "usage": {"input_tokens": 120, "output_tokens": 3}}`
}

func newServer(t *testing.T, handler http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	opts = append([]Option{WithAPIKey("sk-test-" + t.Name()), WithBaseURL(srv.URL + "/"), WithHTTPClient(srv.Client()), WithRetryPolicy(fastRetry(0))}, opts...)
	client, err := NewClient(opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestAskRoundTrip(t *testing.T) {
	t.Parallel()
	var gotBody []byte
	var gotHeader http.Header
	var gotPath string
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set(RequestIDHeader, "req-123")
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	}, WithHeaders(map[string]string{"X-Team": "sentinel", "Authorization": "forged"}))

	req := sampleRequest()
	req.Headers = map[string]string{"X-Call": "one", "User-Agent": "forged"}
	req.Extra = map[string]any{"debug": true}
	resp, err := client.Ask(t.Context(), req)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if gotPath != SystemOnePath {
		t.Errorf("path: got = %q, want = %q", gotPath, SystemOnePath)
	}
	for k, want := range map[string]string{
		"Authorization":      "Bearer sk-test-" + t.Name(),
		"User-Agent":         sdkIdentity,
		"X-Typesafe-Sdk":     sdkIdentity,
		"X-Typesafe-Runtime": runtimeIdentity,
		"Content-Type":       "application/json",
		"Accept":             "application/json",
		"X-Team":             "sentinel",
		"X-Call":             "one",
	} {
		if got := gotHeader.Get(k); got != want {
			t.Errorf("header %s: got = %q, want = %q", k, got, want)
		}
	}
	if gotHeader.Get("X-Typesafe-Retry-Count") != "" {
		t.Errorf("first attempt carries a retry count: %q", gotHeader.Get("X-Typesafe-Retry-Count"))
	}

	var wire map[string]any
	if err := json.Unmarshal(gotBody, &wire); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	wantWire := map[string]any{
		"model": "jev-latest",
		"state": "The install script downloads and executes a remote binary.",
		"debug": true,
		"questions": map[string]any{
			"exfil": map[string]any{"type": "noul", "instructions": "Does the state describe data exfiltration?"},
			"kind": map[string]any{"type": "choice", "instructions": "Classify the behavior.",
				"criteria": map[string]any{"benign": nil, "suspicious": "warrants review", "malicious": "clearly hostile"}},
			"severity": map[string]any{"type": "score", "instructions": "Rate the severity.", "criteria": []any{"none", "low", "high"}},
		},
	}
	if !reflect.DeepEqual(wantWire, wire) {
		t.Errorf("request wire body:\n got: %v\nwant: %v", wire, wantWire)
	}

	want := &Response{
		Model:     "jev-1.13.0",
		Usage:     Usage{InputTokens: 120, OutputTokens: 3},
		RequestID: "req-123",
		Answers: map[string]Answer{
			"exfil": NoulAnswer{Probability: 0.91},
			"kind": ChoiceAnswer{Choice: "malicious",
				Probabilities: map[string]float64{"benign": 0.02, "suspicious": 0.18, "malicious": 0.80}, Confidence: 0.77},
			"severity": ScoreAnswer{Score: 1.6, Legend: map[string]Content{"0": "none", "1": "low", "2": "high"},
				Probabilities: map[string]float64{"0": 0.1, "1": 0.2, "2": 0.7}, Confidence: 0.65},
		},
	}
	if !reflect.DeepEqual(want, resp) {
		t.Errorf("response:\n got: %+v\nwant: %+v", resp, want)
	}

	if noul, err := resp.Noul("exfil"); err != nil || noul.Probability != 0.91 {
		t.Errorf("Noul(exfil): got = (%v, %v)", noul, err)
	}
	if choice, err := resp.Choice("kind"); err != nil || choice.Choice != "malicious" {
		t.Errorf("Choice(kind): got = (%v, %v)", choice, err)
	}
	score, err := resp.Score("severity")
	if err != nil || score.Score != 1.6 {
		t.Errorf("Score(severity): got = (%v, %v)", score, err)
	}
	if level, p := score.Level(); level != 2 || p != 0.7 {
		t.Errorf("Level(): got = (%d, %v), want = (2, 0.7)", level, p)
	}
	if _, err := resp.Choice("exfil"); !errors.Is(err, ErrNoAnswer) {
		t.Errorf("Choice(exfil) on a noul: got = %v, want ErrNoAnswer", err)
	}
	if len(resp.Nouls()) != 1 || len(resp.Choices()) != 1 || len(resp.Scores()) != 1 {
		t.Errorf("grouped accessors: nouls=%d choices=%d scores=%d, want 1 each", len(resp.Nouls()), len(resp.Choices()), len(resp.Scores()))
	}
}

func TestAskRawReturnsUndecodedBody(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(RequestIDHeader, "raw-1")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"exfil":{"type":"noul","noul":0.5,"future_field":1}},"usage":{"input_tokens":1,"output_tokens":1}}`))
	})
	req := Request{State: "s", Questions: map[string]Question{"exfil": Noul{}}}
	body, requestID, err := client.AskRaw(t.Context(), req)
	if err != nil {
		t.Fatalf("AskRaw: %v", err)
	}
	if requestID != "raw-1" || !bytes.Contains(body, []byte("future_field")) {
		t.Errorf("AskRaw: got id=%q body=%s", requestID, body)
	}
}

func TestAskAcceptsPointerAndRawQuestions(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	})
	req := sampleRequest()
	req.Questions = map[string]Question{
		"exfil": &Noul{Instructions: "?"},
		"kind": Raw{"type": "choice", "instructions": "?",
			"criteria": map[string]any{"benign": nil, "suspicious": "warrants review", "malicious": "clearly hostile"}},
		"severity": Raw{"type": "score", "criteria": []any{"none", "low", "high"}},
	}
	resp, err := client.Ask(t.Context(), req)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := resp.Score("severity"); err != nil {
		t.Errorf("Score(severity): %v", err)
	}
}

func TestRawQuestionMarshalsAsGiven(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(Raw{"type": "noul", "instructions": "?", "criteria": map[string]any{"true": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"criteria":{"true":"yes"},"instructions":"?","type":"noul"}`; string(got) != want {
		t.Errorf("Marshal(Raw): got = %s, want = %s", got, want)
	}
}

func TestAskRejectsInvalidRequestBeforeSending(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newServer(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	q := map[string]Question{"q": Noul{}}
	tests := []struct {
		name string
		req  Request
	}{
		{"nil state", Request{Questions: q}},
		{"no questions", Request{State: "s"}},
		{"empty id", Request{State: "s", Questions: map[string]Question{"": Noul{}}}},
		{"nil question", Request{State: "s", Questions: map[string]Question{"q": nil}}},
		{"nil pointer question", Request{State: "s", Questions: map[string]Question{"q": (*Noul)(nil)}}},
		{"no options", Request{State: "s", Questions: map[string]Question{"q": Choice{}}}},
		{"no levels", Request{State: "s", Questions: map[string]Question{"q": Score{}}}},
		{"raw without type", Request{State: "s", Questions: map[string]Question{"q": Raw{"instructions": "?"}}}},
		{"raw unknown type", Request{State: "s", Questions: map[string]Question{"q": Raw{"type": "rank"}}}},
		{"raw choice without criteria", Request{State: "s", Questions: map[string]Question{"q": Raw{"type": "choice"}}}},
		{"raw score criteria not array", Request{State: "s", Questions: map[string]Question{"q": Raw{"type": "score", "criteria": map[string]any{}}}}},
		{"bad per-call retry", Request{State: "s", Questions: q, Retry: &RetryPolicy{Jitter: 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := client.Ask(t.Context(), tt.req); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("Ask error: got = %v, want ErrInvalidRequest", err)
			}
		})
	}
	t.Cleanup(func() {
		if n := calls.Load(); n != 0 {
			t.Errorf("server calls: got = %d, want = 0", n)
		}
	})
}

func TestAskRetriesTransientStatuses(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var retryCounts []string
	statuses := []int{http.StatusTooManyRequests, StatusOverloaded}
	var retries []int
	policy := fastRetry(len(statuses))
	policy.OnRetry = func(attempt int, _ error, _ time.Duration) { retries = append(retries, attempt) }
	var logs bytes.Buffer
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		retryCounts = append(retryCounts, r.Header.Get("X-Typesafe-Retry-Count"))
		n := int(calls.Add(1)) - 1
		if n < len(statuses) {
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(statuses[n])
			return
		}
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	}, WithRetryPolicy(policy), WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	if _, err := client.Ask(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := calls.Load(); got != int32(len(statuses)+1) {
		t.Errorf("attempts: got = %d, want = %d", got, len(statuses)+1)
	}
	if !reflect.DeepEqual(retries, []int{1, 2}) {
		t.Errorf("OnRetry attempts: got = %v, want = [1 2]", retries)
	}
	if !reflect.DeepEqual(retryCounts, []string{"", "1", "2"}) {
		t.Errorf("X-Typesafe-Retry-Count per attempt: got = %q", retryCounts)
	}
	if s := logs.String(); !strings.Contains(s, "jev: retrying") || strings.Contains(s, "sk-test-") || strings.Contains(s, "install script") {
		t.Errorf("logs: want retry entries without key or state, got:\n%s", s)
	}
}

func TestAskPerCallRetryOverride(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}, WithRetryPolicy(fastRetry(3)))
	req := sampleRequest()
	req.Retry = &RetryPolicy{}
	if _, err := client.Ask(t.Context(), req); !errors.Is(err, ErrServer) {
		t.Errorf("Ask error: got = %v, want ErrServer", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts with retries disabled per call: got = %d, want = 1", got)
	}
}

func TestAskRespectsRetryBudget(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	policy := fastRetry(5)
	policy.Budget = 10 * time.Second
	client.retry = policy
	// Retry-After of 60s exceeds the 10s budget, so no retry is attempted.
	if _, err := client.Ask(t.Context(), sampleRequest()); !errors.Is(err, ErrRateLimited) {
		t.Errorf("Ask error: got = %v, want ErrRateLimited", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts: got = %d, want = 1", got)
	}
}

func TestAskSurfacesAPIError(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set(RequestIDHeader, "req-9")
		w.WriteHeader(StatusOverloaded)
		_, _ = w.Write([]byte(`{"error":"overloaded"}`))
	})
	_, err := client.Ask(t.Context(), sampleRequest())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Ask error: got = %v, want *APIError", err)
	}
	if apiErr.StatusCode != StatusOverloaded || apiErr.RetryAfter != 7*time.Second || apiErr.RequestID != "req-9" || apiErr.Endpoint != "POST /v1/systemone" {
		t.Errorf("APIError fields: %+v", apiErr)
	}
	if want := `jev: POST /v1/systemone: HTTP 529 Overloaded: overloaded (request_id=req-9)`; err.Error() != want {
		t.Errorf("Error(): got = %q, want = %q", err.Error(), want)
	}
	if !errors.Is(err, ErrServer) || errors.Is(err, ErrRateLimited) {
		t.Errorf("status class: ErrServer=%v ErrRateLimited=%v", errors.Is(err, ErrServer), errors.Is(err, ErrRateLimited))
	}
	if strings.Contains(err.Error(), "sk-test-") {
		t.Errorf("error leaks the API key: %q", err.Error())
	}
}

func TestAPIErrorStatusClasses(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]error{
		400: ErrBadRequest, 401: ErrUnauthorized, 403: ErrForbidden, 404: ErrNotFound,
		422: ErrUnprocessable, 429: ErrRateLimited, 500: ErrServer, 529: ErrServer,
	} {
		err := error(&APIError{StatusCode: status})
		if !errors.Is(err, want) {
			t.Errorf("status %d: errors.Is(%v) = false", status, want)
		}
	}
	if errors.Is(&APIError{StatusCode: 418}, ErrBadRequest) {
		t.Error("418 matched ErrBadRequest")
	}
}

func TestAskDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":[{"loc":["body","questions","urgency","criteria"],"msg":"Field required","type":"missing"}]}`))
	}, WithRetryPolicy(fastRetry(3)))
	_, err := client.Ask(t.Context(), sampleRequest())
	if !errors.Is(err, ErrUnprocessable) {
		t.Errorf("Ask error: got = %v, want ErrUnprocessable", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Message != "questions.urgency.criteria: Field required" {
		t.Errorf("Message: got = %q", apiErr.Message)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts: got = %d, want = 1", got)
	}
}

func TestAskValidatesAnswersAgainstQuestions(t *testing.T) {
	t.Parallel()
	rep := func(old, new string) string { return strings.Replace(sampleAnswers, old, new, 1) }
	tests := []struct {
		name    string
		answers string
	}{
		{"missing answer", `{"exfil": {"type": "noul", "noul": 0.5}}`},
		{"extra answer", strings.TrimSuffix(sampleAnswers, "}") + `, "bonus": {"type": "noul", "noul": 0.5}}`},
		{"kind mismatch", rep(`"type": "noul", "noul": 0.91`, `"type": "choice", "choice": "x", "confidence": 1`)},
		{"noul above one", rep(`"noul": 0.91`, `"noul": 1.5`)},
		{"noul missing", rep(`"noul": 0.91`, `"nope": 0.91`)},
		{"undeclared choice", rep(`"choice": "malicious"`, `"choice": "greyware"`)},
		{"undeclared option probability", rep(`"benign": 0.02`, `"greyware": 0.02`)},
		{"choice probabilities absent", rep(`"probabilities": {"benign": 0.02, "suspicious": 0.18, "malicious": 0.80}, `, ``)},
		{"choice probabilities incomplete", rep(`"benign": 0.02, `, ``)},
		{"choice probabilities do not sum to one", rep(`"malicious": 0.80`, `"malicious": 0.50`)},
		{"choice confidence missing", rep(`"confidence": 0.77`, `"confidence": null`)},
		{"score above rubric", rep(`"score": 1.6`, `"score": 2.4`)},
		{"score missing", rep(`"score": 1.6`, `"score": null`)},
		{"score level out of range", rep(`"2": 0.7`, `"3": 0.7`)},
		{"score level keyed by description", rep(`"2": 0.7`, `"high": 0.7`)},
		{"score level key not canonical", rep(`"2": 0.7`, `"02": 0.7`)},
		{"score probabilities incomplete", rep(`"0": 0.1, `, ``)},
		{"score probabilities do not sum to one", rep(`"2": 0.7`, `"2": 0.4`)},
		{"score legend absent", rep(`"legend": {"0": "none", "1": "low", "2": "high"}, `, ``)},
		{"score legend mismatched", rep(`"2": "high"`, `"3": "high"`)},
		{"not json", `nope`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(sampleBody(tt.answers)))
			})
			if _, err := client.Ask(t.Context(), sampleRequest()); !errors.Is(err, ErrResponseValidation) {
				t.Errorf("Ask error: got = %v, want ErrResponseValidation", err)
			}
		})
	}
}

func TestAskAcceptsStructuredLegendAndSingleLevel(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","answers":{"s":{"type":"score","score":0,"legend":{"0":{"label":"only"}},"probabilities":{"0":1},"confidence":0.9}},"usage":{}}`))
	})
	resp, err := client.Ask(t.Context(), Request{State: "s", Questions: map[string]Question{"s": Score{Levels: []Content{map[string]any{"label": "only"}}}}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	score, _ := resp.Score("s")
	if legend, ok := score.Legend["0"].(map[string]any); !ok || legend["label"] != "only" {
		t.Errorf("structured legend: got = %#v", score.Legend["0"])
	}
}

func TestAskCapsResponseSize(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	}, WithMaxResponseBytes(64))
	if _, err := client.Ask(t.Context(), sampleRequest()); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("Ask error: got = %v, want ErrResponseTooLarge", err)
	}
}

func TestAskRetriesHTTPTimeoutWhileCallerContextLive(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(WithAPIKey("k"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithTimeout(200*time.Millisecond), WithRetryPolicy(fastRetry(1)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Ask(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("attempts: got = %d, want = 2", got)
	}
}

func TestAskClassifiesTimeoutAndConnectionErrors(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	defer close(release)
	client := newServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}, WithTimeout(100*time.Millisecond))
	_, err := client.Ask(t.Context(), sampleRequest())
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, ErrConnection) {
		t.Errorf("timeout classification: got = %v", err)
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	refused, err := NewClient(WithAPIKey("k"), WithBaseURL(srv.URL), WithRetryPolicy(RetryPolicy{}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = refused.Ask(t.Context(), sampleRequest())
	if !errors.Is(err, ErrConnection) || errors.Is(err, ErrTimeout) {
		t.Errorf("connection classification: got = %v", err)
	}
	if !DefaultRetryPolicy().retryable(err) {
		t.Error("default policy does not retry a connection error")
	}
}

func TestAskDoesNotRetryAfterCallerDeadline(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	client := newServer(t, func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}, WithRetryPolicy(fastRetry(3)))
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := client.Ask(ctx, sampleRequest()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Ask error: got = %v, want context.DeadlineExceeded", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts: got = %d, want = 1", got)
	}
}

func TestAskStopsWhenContextCanceledDuringBackoff(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}, WithRetryPolicy(RetryPolicy{MaxRetries: 3, BaseDelay: time.Hour, MaxDelay: time.Hour}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := client.Ask(ctx, sampleRequest())
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Ask error: got = %v, want context.Canceled", err)
	}
}

func TestListModels(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != ModelsPath || r.Header.Get("Content-Type") != "" {
			http.Error(w, "bad request shape", http.StatusBadRequest)
			return
		}
		w.Header().Set(RequestIDHeader, "m-1")
		_, _ = w.Write([]byte(`{"models":[{"name":"jev-latest","description":"General-purpose system one model.","release_date":"2026-09-15"}]}`))
	})
	list, err := client.ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := &ModelList{RequestID: "m-1", Models: []ModelMetadata{{Name: "jev-latest", Description: "General-purpose system one model.", ReleaseDate: "2026-09-15"}}}
	if !reflect.DeepEqual(want, list) {
		t.Errorf("ListModels: got = %+v, want = %+v", list, want)
	}
}

func TestListModelsRejectsMalformedBody(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"description":"no name"}]}`))
	})
	if _, err := client.ListModels(t.Context()); !errors.Is(err, ErrResponseValidation) {
		t.Errorf("ListModels error: got = %v, want ErrResponseValidation", err)
	}
}

func TestNewClientEnvironment(t *testing.T) {
	t.Setenv(APIKeyEnv, " ")
	t.Setenv(BaseURLEnv, "")
	t.Setenv(DefaultModelEnv, "")
	if _, err := NewClient(); err == nil {
		t.Error("NewClient with blank key: got nil error")
	}
	t.Setenv(APIKeyEnv, "sk-env")
	t.Setenv(BaseURLEnv, "https://proxy.example/")
	t.Setenv(DefaultModelEnv, " jev-preview ")
	client, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.apiKey != "sk-env" || client.baseURL != "https://proxy.example" || client.model != "jev-preview" || client.httpClient.Timeout != DefaultTimeout {
		t.Errorf("env defaults: key=%q base=%q model=%q timeout=%v", client.apiKey, client.baseURL, client.model, client.httpClient.Timeout)
	}
	explicit, err := NewClient(WithAPIKey("sk-opt"), WithBaseURL("http://127.0.0.1:1"), WithModel("jev-1.13.0"))
	if err != nil {
		t.Fatalf("NewClient explicit: %v", err)
	}
	if explicit.apiKey != "sk-opt" || explicit.baseURL != "http://127.0.0.1:1" || explicit.model != "jev-1.13.0" {
		t.Errorf("options should win over env: %+v", explicit)
	}
	t.Setenv(BaseURLEnv, "not a url")
	if _, err := NewClient(); err == nil {
		t.Error("NewClient with invalid base url env: got nil error")
	}
}

func TestNewClientOptions(t *testing.T) {
	t.Parallel()
	shared := &http.Client{Timeout: time.Minute}
	for _, tt := range []struct {
		name    string
		opts    []Option
		wantErr bool
	}{
		{"explicit key", []Option{WithAPIKey("k")}, false},
		{"empty key", []Option{WithAPIKey("")}, true},
		{"empty model", []Option{WithAPIKey("k"), WithModel("")}, true},
		{"relative base url", []Option{WithAPIKey("k"), WithBaseURL("/v1")}, true},
		{"ftp base url", []Option{WithAPIKey("k"), WithBaseURL("ftp://x/y")}, true},
		{"nil http client", []Option{WithAPIKey("k"), WithHTTPClient(nil)}, true},
		{"zero timeout", []Option{WithAPIKey("k"), WithTimeout(0)}, true},
		{"negative retries", []Option{WithAPIKey("k"), WithRetryPolicy(RetryPolicy{MaxRetries: -1})}, true},
		{"jitter above one", []Option{WithAPIKey("k"), WithRetryPolicy(RetryPolicy{Jitter: 1.5})}, true},
		{"zero size cap", []Option{WithAPIKey("k"), WithMaxResponseBytes(0)}, true},
		{"shared client with timeout override", []Option{WithAPIKey("k"), WithHTTPClient(shared), WithTimeout(time.Second)}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewClient(tt.opts...); (err != nil) != tt.wantErr {
				t.Errorf("NewClient error: got = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
	if shared.Timeout != time.Minute {
		t.Errorf("WithTimeout mutated the caller's http.Client: %v", shared.Timeout)
	}
}
