// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"time"
)

// DefaultEndpoint is TypeSafe AI's hosted System One endpoint.
const DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"

// APIKeyEnv is the environment variable [NewClient] reads when no key is
// supplied explicitly.
const APIKeyEnv = "TYPESAFE_API_KEY"

// Model aliases published by TypeSafe AI. Exact versioned ids such as
// "jev-1.13.0" are also accepted; the aliases are the stable surface.
const (
	// ModelJevLatest is the most recent stable, official Jev release.
	ModelJevLatest = "jev-latest"
	// ModelJevPreview is the most recent Jev release, official or not.
	ModelJevPreview = "jev-preview"
)

const (
	defaultTimeout          = 30 * time.Second
	defaultMaxResponseBytes = 16 << 20
	userAgent               = "jev-go-sdk (+https://github.com/ajayk/jev-go-sdk)"
)

// Request is one System One call.
type Request struct {
	// Model is the model id or alias. Empty selects the client's model,
	// which defaults to [ModelJevLatest].
	Model string `json:"model"`
	// State is the content every question is evaluated against: a string,
	// or a value that marshals to a JSON object or array.
	State Content `json:"state"`
	// Questions maps caller-chosen ids to questions. Answers come back under
	// the same ids.
	Questions map[string]Question `json:"questions"`
}

// Validate reports whether r can be sent. Every failure wraps
// ErrInvalidRequest.
func (r Request) Validate() error {
	if r.Model == "" {
		return fmt.Errorf("%w: model must not be empty", ErrInvalidRequest)
	}
	if r.State == nil {
		return fmt.Errorf("%w: state must not be nil", ErrInvalidRequest)
	}
	if len(r.Questions) == 0 {
		return fmt.Errorf("%w: at least one question is required", ErrInvalidRequest)
	}
	for id, q := range r.Questions {
		if id == "" {
			return fmt.Errorf("%w: question id must not be empty", ErrInvalidRequest)
		}
		q, err := normalizeQuestion(q)
		if err != nil {
			return fmt.Errorf("%w: question %q: %w", ErrInvalidRequest, id, err)
		}
		if err := q.validate(); err != nil {
			return fmt.Errorf("%w: question %q: %w", ErrInvalidRequest, id, err)
		}
	}
	return nil
}

// Response is the answer set for one Request.
type Response struct {
	// Model is the model that served the request, with aliases resolved as
	// the API reports them.
	Model string
	// Answers holds one answer per question id in the request.
	Answers map[string]Answer
	// Usage is the token accounting for the call.
	Usage Usage
}

// Noul returns the answer to the [Noul] question id, or ErrNoAnswer.
func (r *Response) Noul(id string) (NoulAnswer, error) {
	return typedAnswer[NoulAnswer](r, id)
}

// Choice returns the answer to the [Choice] question id, or ErrNoAnswer.
func (r *Response) Choice(id string) (ChoiceAnswer, error) {
	return typedAnswer[ChoiceAnswer](r, id)
}

// Score returns the answer to the [Score] question id, or ErrNoAnswer.
func (r *Response) Score(id string) (ScoreAnswer, error) {
	return typedAnswer[ScoreAnswer](r, id)
}

func typedAnswer[T Answer](r *Response, id string) (T, error) {
	var zero T
	if r == nil {
		return zero, fmt.Errorf("%w: nil response", ErrNoAnswer)
	}
	a, ok := r.Answers[id].(T)
	if !ok {
		return zero, fmt.Errorf("%w: question %q", ErrNoAnswer, id)
	}
	return a, nil
}

// Usage is the API's token accounting. Output tokens are reported but not
// billed by the provider.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type wireResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   Usage                      `json:"usage"`
}

// RetryPolicy bounds the retries [Client.Ask] makes on transient failures.
// Delay for attempt n is BaseDelay doubled n times, capped at MaxDelay, plus
// up to MaxJitter; a Retry-After header longer than that replaces it.
type RetryPolicy struct {
	// MaxRetries is the number of additional attempts after the first. Zero
	// disables retries.
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	MaxJitter  time.Duration
	// OnRetry, if set, is called before each retry with the attempt number
	// (starting at 1), the error being retried, and the delay to be slept.
	OnRetry func(attempt int, err error, delay time.Duration)
}

// DefaultRetryPolicy suits the API's sub-second latency: two short backoffs.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 2, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second, MaxJitter: 250 * time.Millisecond}
}

func (p RetryPolicy) validate() error {
	if p.MaxRetries < 0 || p.BaseDelay < 0 || p.MaxDelay < 0 || p.MaxJitter < 0 {
		return errors.New("retry policy values must not be negative")
	}
	return nil
}

func (p RetryPolicy) delay(attempt int, retryAfter time.Duration) time.Duration {
	d := p.BaseDelay
	for range attempt {
		if d >= p.MaxDelay/2 {
			d = p.MaxDelay
			break
		}
		d *= 2
	}
	d = min(d, p.MaxDelay)
	if p.MaxJitter > 0 {
		if n, err := rand.Int(rand.Reader, big.NewInt(int64(p.MaxJitter))); err == nil {
			d += time.Duration(n.Int64())
		}
	}
	return max(d, retryAfter)
}

// Client calls the System One API. Construct one with [NewClient]; a Client is
// safe for concurrent use once constructed.
type Client struct {
	endpoint         string
	apiKey           string
	model            string
	httpClient       *http.Client
	retry            RetryPolicy
	maxResponseBytes int64
}

// Option configures a Client.
type Option func(*Client) error

// WithAPIKey supplies the API key explicitly instead of reading [APIKeyEnv].
func WithAPIKey(key string) Option {
	return func(c *Client) error {
		if key == "" {
			return errors.New("api key must not be empty")
		}
		c.apiKey = key
		return nil
	}
}

// WithModel sets the model used by requests that leave Model empty. The
// default is [ModelJevLatest].
func WithModel(model string) Option {
	return func(c *Client) error {
		if model == "" {
			return errors.New("model must not be empty")
		}
		c.model = model
		return nil
	}
}

// WithEndpoint overrides the API endpoint. The URL must be absolute with an
// http or https scheme.
func WithEndpoint(endpoint string) Option {
	return func(c *Client) error {
		u, err := url.Parse(endpoint)
		if err != nil {
			return fmt.Errorf("endpoint: %w", err)
		}
		if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("endpoint %q must be an absolute http(s) URL", endpoint)
		}
		c.endpoint = endpoint
		return nil
	}
}

// WithHTTPClient supplies the HTTP client, including any timeout or
// transport. The default has a 30-second timeout.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) error {
		if httpClient == nil {
			return errors.New("http client must not be nil")
		}
		c.httpClient = httpClient
		return nil
	}
}

// WithRetryPolicy overrides the retry policy.
func WithRetryPolicy(policy RetryPolicy) Option {
	return func(c *Client) error {
		if err := policy.validate(); err != nil {
			return err
		}
		c.retry = policy
		return nil
	}
}

// WithMaxResponseBytes caps the response body the client is willing to read.
func WithMaxResponseBytes(limit int64) Option {
	return func(c *Client) error {
		if limit <= 0 {
			return errors.New("max response bytes must be positive")
		}
		c.maxResponseBytes = limit
		return nil
	}
}

// NewClient constructs a Client. The API key comes from [WithAPIKey] or, when
// that option is absent, from the [APIKeyEnv] environment variable.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		endpoint:         DefaultEndpoint,
		model:            ModelJevLatest,
		httpClient:       &http.Client{Timeout: defaultTimeout},
		retry:            DefaultRetryPolicy(),
		maxResponseBytes: defaultMaxResponseBytes,
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if c.apiKey == "" {
		c.apiKey = os.Getenv(APIKeyEnv)
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("api key is required: pass WithAPIKey or set %s", APIKeyEnv)
	}
	return c, nil
}

// Ask sends req and returns its validated answers. Transient failures are
// retried per the client's policy while ctx is live; the returned error is
// the last attempt's. Every non-2xx status surfaces as an *APIError, and a
// 2xx body that does not match the questions as sent surfaces as
// ErrResponseValidation.
func (c *Client) Ask(ctx context.Context, req Request) (*Response, error) {
	req.Model = cmp.Or(req.Model, c.model)
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding: %w", ErrInvalidRequest, err)
	}

	for attempt := 0; ; attempt++ {
		resp, err := c.do(ctx, body, req.Questions)
		if err == nil {
			return resp, nil
		}
		if attempt >= c.retry.MaxRetries || ctx.Err() != nil || !IsRetryable(err) {
			return nil, err
		}
		var retryAfter time.Duration
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			retryAfter = apiErr.RetryAfter
		}
		delay := c.retry.delay(attempt, retryAfter)
		if c.retry.OnRetry != nil {
			c.retry.OnRetry(attempt+1, err, delay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("jev: retry interrupted: %w (last error: %w)", ctx.Err(), err)
		case <-timer.C:
		}
	}
}

func (c *Client) do(ctx context.Context, body []byte, questions map[string]Question) (*Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev: building request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("jev: %w", err)
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jev: reading response: %w", err)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrResponseTooLarge, c.maxResponseBytes)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, &APIError{
			StatusCode: httpResp.StatusCode,
			Body:       bodyExcerpt(raw),
			RetryAfter: parseRetryAfter(httpResp.Header.Get("Retry-After")),
		}
	}
	return decodeResponse(raw, questions)
}

// decodeResponse decodes a 2xx body and checks it against the questions as
// sent: every question has exactly one answer of its own kind and no answer
// arrives for a question that was not asked.
func decodeResponse(raw []byte, questions map[string]Question) (*Response, error) {
	var w wireResponse
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, validationError("decoding body: %w", err)
	}
	resp := &Response{Model: w.Model, Usage: w.Usage, Answers: make(map[string]Answer, len(questions))}
	for id, question := range questions {
		rawAnswer, ok := w.Answers[id]
		if !ok {
			return nil, validationError("no answer for question %q", id)
		}
		answer, err := decodeAnswer(id, rawAnswer, question)
		if err != nil {
			return nil, err
		}
		resp.Answers[id] = answer
	}
	for id := range w.Answers {
		if _, ok := questions[id]; !ok {
			return nil, validationError("answer %q has no matching question", id)
		}
	}
	return resp, nil
}
