// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetry(maxRetries int) RetryPolicy {
	return RetryPolicy{MaxRetries: maxRetries, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
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
	opts = append([]Option{WithAPIKey("sk-test-" + t.Name()), WithEndpoint(srv.URL), WithHTTPClient(srv.Client()), WithRetryPolicy(fastRetry(0))}, opts...)
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
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	})

	resp, err := client.Ask(t.Context(), sampleRequest())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got, want := gotHeader.Get("Authorization"), "Bearer sk-test-"+t.Name(); got != want {
		t.Errorf("Authorization: got = %q, want = %q", got, want)
	}
	if got := gotHeader.Get("User-Agent"); !strings.HasPrefix(got, "jev-go-sdk") {
		t.Errorf("User-Agent: got = %q", got)
	}

	var wire map[string]any
	if err := json.Unmarshal(gotBody, &wire); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	wantWire := map[string]any{
		"model": "jev-latest",
		"state": "The install script downloads and executes a remote binary.",
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
		Model: "jev-1.13.0",
		Usage: Usage{InputTokens: 120, OutputTokens: 3},
		Answers: map[string]Answer{
			"exfil": NoulAnswer{Probability: 0.91},
			"kind": ChoiceAnswer{Choice: "malicious",
				Probabilities: map[string]float64{"benign": 0.02, "suspicious": 0.18, "malicious": 0.80}, Confidence: 0.77},
			"severity": ScoreAnswer{Score: 1.6, Legend: map[string]string{"0": "none", "1": "low", "2": "high"},
				Probabilities: map[string]float64{"0": 0.1, "1": 0.2, "2": 0.7}, Confidence: 0.65},
		},
	}
	if !reflect.DeepEqual(want, resp) {
		t.Errorf("response:\n got: %+v\nwant: %+v", resp, want)
	}

	noul, err := resp.Noul("exfil")
	if err != nil || noul.Probability != 0.91 {
		t.Errorf("Noul(exfil): got = (%v, %v)", noul, err)
	}
	choice, err := resp.Choice("kind")
	if err != nil || choice.Choice != "malicious" {
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
	if _, err := resp.Noul("nope"); !errors.Is(err, ErrNoAnswer) {
		t.Errorf("Noul(nope): got = %v, want ErrNoAnswer", err)
	}
}

func TestAskAcceptsPointerQuestions(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	})
	req := sampleRequest()
	req.Questions = map[string]Question{
		"exfil":    &Noul{Instructions: "?"},
		"kind":     &Choice{Instructions: "?", Options: req.Questions["kind"].(Choice).Options},
		"severity": &Score{Instructions: "?", Levels: req.Questions["severity"].(Score).Levels},
	}
	if _, err := client.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestAskRejectsInvalidRequestBeforeSending(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newServer(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	q := map[string]Question{"q": Noul{Instructions: "i"}}
	tests := []struct {
		name string
		req  Request
	}{
		{"nil state", Request{Questions: q}},
		{"no questions", Request{State: "s"}},
		{"empty id", Request{State: "s", Questions: map[string]Question{"": Noul{Instructions: "i"}}}},
		{"nil question", Request{State: "s", Questions: map[string]Question{"q": nil}}},
		{"nil pointer question", Request{State: "s", Questions: map[string]Question{"q": (*Noul)(nil)}}},
		{"one option", Request{State: "s", Questions: map[string]Question{"q": Choice{Instructions: "i", Options: map[string]Content{"a": nil}}}}},
		{"one level", Request{State: "s", Questions: map[string]Question{"q": Score{Instructions: "i", Levels: []Content{"a"}}}}},
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

func TestAskRetriesTransientStatusesAndHonorsRetryAfter(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	statuses := []int{http.StatusTooManyRequests, StatusOverloaded}
	var retries []struct {
		attempt int
		delay   time.Duration
	}
	policy := fastRetry(len(statuses))
	policy.OnRetry = func(attempt int, _ error, delay time.Duration) {
		retries = append(retries, struct {
			attempt int
			delay   time.Duration
		}{attempt, delay})
	}
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1)) - 1
		if n < len(statuses) {
			if n == 0 {
				w.Header().Set("Retry-After", "0")
			}
			w.WriteHeader(statuses[n])
			return
		}
		_, _ = w.Write([]byte(sampleBody(sampleAnswers)))
	}, WithRetryPolicy(policy))

	if _, err := client.Ask(t.Context(), sampleRequest()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := calls.Load(); got != int32(len(statuses)+1) {
		t.Errorf("attempts: got = %d, want = %d", got, len(statuses)+1)
	}
	if len(retries) != 2 || retries[0].attempt != 1 || retries[1].attempt != 2 {
		t.Errorf("OnRetry calls: got = %+v", retries)
	}
}

func TestRetryPolicyDelay(t *testing.T) {
	t.Parallel()
	p := RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	for _, tt := range []struct {
		attempt    int
		retryAfter time.Duration
		want       time.Duration
	}{
		{0, 0, 100 * time.Millisecond},
		{1, 0, 200 * time.Millisecond},
		{2, 0, 400 * time.Millisecond},
		{3, 0, 800 * time.Millisecond},
		{4, 0, time.Second},
		{50, 0, time.Second},
		{0, 3 * time.Second, 3 * time.Second},
	} {
		if got := p.delay(tt.attempt, tt.retryAfter); got != tt.want {
			t.Errorf("delay(%d, %v): got = %v, want = %v", tt.attempt, tt.retryAfter, got, tt.want)
		}
	}
}

func TestAskSurfacesAPIError(t *testing.T) {
	t.Parallel()
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(StatusOverloaded)
		_, _ = w.Write([]byte("{\"error\":\"overloaded\n\x1b[31m\"}"))
	})
	_, err := client.Ask(t.Context(), sampleRequest())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Ask error: got = %v, want *APIError", err)
	}
	want := &APIError{StatusCode: StatusOverloaded, Body: `{"error":"overloaded [31m"}`, RetryAfter: 7 * time.Second}
	if !reflect.DeepEqual(want, apiErr) {
		t.Errorf("APIError: got = %+v, want = %+v", apiErr, want)
	}
	if got, want := err.Error(), `jev: HTTP 529 Overloaded: {"error":"overloaded [31m"}`; got != want {
		t.Errorf("Error(): got = %q, want = %q", got, want)
	}
	if strings.Contains(err.Error(), "sk-test-") {
		t.Errorf("error leaks the API key: %q", err.Error())
	}
}

func TestAskDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, `{"detail":"bad"}`, http.StatusUnprocessableEntity)
	}, WithRetryPolicy(fastRetry(3)))
	_, err := client.Ask(t.Context(), sampleRequest())
	if IsRetryable(err) {
		t.Errorf("IsRetryable: got = true, want = false (%v)", err)
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
			_, err := client.Ask(t.Context(), sampleRequest())
			if !errors.Is(err, ErrResponseValidation) {
				t.Errorf("Ask error: got = %v, want ErrResponseValidation", err)
			}
		})
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
	httpClient := srv.Client()
	httpClient.Timeout = 200 * time.Millisecond
	client, err := NewClient(WithAPIKey("k"), WithEndpoint(srv.URL), WithHTTPClient(httpClient), WithRetryPolicy(fastRetry(1)))
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

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", &APIError{StatusCode: 429}, true},
		{"408", &APIError{StatusCode: 408}, true},
		{"500", &APIError{StatusCode: 500}, true},
		{"529", &APIError{StatusCode: StatusOverloaded}, true},
		{"401", &APIError{StatusCode: 401}, false},
		{"422", &APIError{StatusCode: 422}, false},
		{"validation", validationError("bad"), false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"http client timeout", &url.Error{Op: "Post", Err: timeoutErr{}}, true},
		{"http deadline mid-request", &url.Error{Op: "Post", Err: context.DeadlineExceeded}, true},
		{"http canceled", &url.Error{Op: "Post", Err: context.Canceled}, false},
		{"http refused", &url.Error{Op: "Post", Err: errors.New("refused")}, false},
	} {
		if got := IsRetryable(tt.err); got != tt.want {
			t.Errorf("IsRetryable(%s): got = %v, want = %v", tt.name, got, tt.want)
		}
	}
}

func TestNewClientReadsKeyFromEnvironment(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	if _, err := NewClient(); err == nil {
		t.Error("NewClient with no key: got nil error")
	}
	t.Setenv(APIKeyEnv, "sk-env")
	client, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.apiKey != "sk-env" || client.model != ModelJevLatest || client.endpoint != DefaultEndpoint {
		t.Errorf("defaults: %+v", client)
	}
}

func TestNewClientOptions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		opts    []Option
		wantErr bool
	}{
		{"explicit key", []Option{WithAPIKey("k")}, false},
		{"empty key", []Option{WithAPIKey("")}, true},
		{"empty model", []Option{WithAPIKey("k"), WithModel("")}, true},
		{"relative endpoint", []Option{WithAPIKey("k"), WithEndpoint("/v1")}, true},
		{"ftp endpoint", []Option{WithAPIKey("k"), WithEndpoint("ftp://x/y")}, true},
		{"nil http client", []Option{WithAPIKey("k"), WithHTTPClient(nil)}, true},
		{"negative retries", []Option{WithAPIKey("k"), WithRetryPolicy(RetryPolicy{MaxRetries: -1})}, true},
		{"zero size cap", []Option{WithAPIKey("k"), WithMaxResponseBytes(0)}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewClient(tt.opts...); (err != nil) != tt.wantErr {
				t.Errorf("NewClient error: got = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestQuestionMarshalJSON(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		question Question
		want     string
	}{
		{Noul{Instructions: "Is it raining?"}, `{"type":"noul","instructions":"Is it raining?"}`},
		{Noul{Instructions: "Is it raining?", True: "wet"}, `{"type":"noul","instructions":"Is it raining?","criteria":{"true":"wet"}}`},
		{Choice{Instructions: "Pick.", Options: map[string]Content{"a": nil, "b": "second"}}, `{"type":"choice","instructions":"Pick.","criteria":{"a":null,"b":"second"}}`},
		{Score{Instructions: "Rate.", Levels: []Content{"low", "high"}}, `{"type":"score","instructions":"Rate.","criteria":["low","high"]}`},
	} {
		got, err := json.Marshal(tt.question)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(got) != tt.want {
			t.Errorf("Marshal(%T): got = %s, want = %s", tt.question, got, tt.want)
		}
	}
}

func TestScoreAnswerLevel(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		probabilities map[string]float64
		wantLevel     int
		wantP         float64
	}{
		{nil, -1, 0},
		{map[string]float64{"0": 1}, 0, 1},
		{map[string]float64{"0": 0.1, "1": 0.6, "2": 0.3}, 1, 0.6},
		{map[string]float64{"2": 0.5, "1": 0.5}, 1, 0.5},
	} {
		level, p := (ScoreAnswer{Probabilities: tt.probabilities}).Level()
		if level != tt.wantLevel || p != tt.wantP {
			t.Errorf("Level(%v): got = (%d, %v), want = (%d, %v)", tt.probabilities, level, p, tt.wantLevel, tt.wantP)
		}
	}
}
