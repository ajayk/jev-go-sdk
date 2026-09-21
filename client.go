// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables read by [NewClient] when the matching option is
// absent. Empty or whitespace-only values are ignored.
const (
	APIKeyEnv       = "TYPESAFE_API_KEY"
	BaseURLEnv      = "TYPESAFE_BASE_URL"
	DefaultModelEnv = "TYPESAFE_DEFAULT_MODEL"
)

// DefaultBaseURL is TypeSafe AI's hosted API root.
const DefaultBaseURL = "https://api.typesafe.ai"

// API paths under the base URL.
const (
	SystemOnePath = "/v1/systemone"
	ModelsPath    = "/v1/models"
)

// Model aliases published by TypeSafe AI. Exact versioned ids such as
// "jev-1.13.0" are also accepted; [Client.ListModels] returns the current set.
const (
	// ModelJevLatest is the most recent stable, official Jev release, and the
	// default model.
	ModelJevLatest = "jev-latest"
	// ModelJevPreview is the most recent Jev release, official or not.
	ModelJevPreview = "jev-preview"
)

// DefaultTimeout is the HTTP timeout applied to each attempt when no HTTP
// client or [WithTimeout] is supplied.
const DefaultTimeout = 10 * time.Second

const defaultMaxResponseBytes = 16 << 20

// protectedHeaders cannot be overridden by WithHeaders or Request.Headers.
var protectedHeaders = []string{"Authorization", "Accept", "User-Agent", "X-Typesafe-Sdk", "X-Typesafe-Runtime", "X-Typesafe-Retry-Count"}

// Request is one System One call.
type Request struct {
	// Model is the model id or alias. Empty selects the client's default
	// model.
	Model string
	// State is the content every question is evaluated against: a string,
	// or a value that marshals to a JSON object or array.
	State Content
	// Questions maps caller-chosen ids to questions. Answers come back under
	// the same ids.
	Questions map[string]Question
	// Extra holds additional top-level body fields, shallow-merged over the
	// body after model, state, and questions are set. A key that collides
	// with one of those replaces it. Use it for API fields this SDK does not
	// model yet.
	Extra map[string]any
	// Headers are additional request headers for this call. Protected
	// headers (authentication, SDK identification, Accept) are not
	// overridable.
	Headers map[string]string
	// Retry, if set, replaces the client's retry policy for this call.
	Retry *RetryPolicy
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
	if r.Retry != nil {
		if err := r.Retry.validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
	}
	return nil
}

// body renders the wire body.
func (r Request) body() ([]byte, error) {
	fields := map[string]any{"model": r.Model, "state": r.State, "questions": r.Questions}
	maps.Copy(fields, r.Extra)
	return json.Marshal(fields)
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
	// RequestID is the server's request id, or "" when absent.
	RequestID string
}

// Noul returns the answer to the [Noul] question id, or ErrNoAnswer.
func (r *Response) Noul(id string) (NoulAnswer, error) { return typedAnswer[NoulAnswer](r, id) }

// Choice returns the answer to the [Choice] question id, or ErrNoAnswer.
func (r *Response) Choice(id string) (ChoiceAnswer, error) { return typedAnswer[ChoiceAnswer](r, id) }

// Score returns the answer to the [Score] question id, or ErrNoAnswer.
func (r *Response) Score(id string) (ScoreAnswer, error) { return typedAnswer[ScoreAnswer](r, id) }

// Nouls returns every [NoulAnswer] keyed by question id.
func (r *Response) Nouls() map[string]NoulAnswer { return answersOf[NoulAnswer](r) }

// Choices returns every [ChoiceAnswer] keyed by question id.
func (r *Response) Choices() map[string]ChoiceAnswer { return answersOf[ChoiceAnswer](r) }

// Scores returns every [ScoreAnswer] keyed by question id.
func (r *Response) Scores() map[string]ScoreAnswer { return answersOf[ScoreAnswer](r) }

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

func answersOf[T Answer](r *Response) map[string]T {
	out := make(map[string]T)
	if r == nil {
		return out
	}
	for id, a := range r.Answers {
		if t, ok := a.(T); ok {
			out[id] = t
		}
	}
	return out
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

// ModelMetadata describes one model available to the account.
type ModelMetadata struct {
	// Name is the id or alias accepted by Request.Model.
	Name string `json:"name"`
	// Description is a human-readable summary of the model.
	Description string `json:"description"`
	// ReleaseDate is the release date, formatted YYYY-MM-DD.
	ReleaseDate string `json:"release_date"`
}

// ModelList is the result of [Client.ListModels].
type ModelList struct {
	// Models holds the available models and aliases.
	Models []ModelMetadata `json:"models"`
	// RequestID is the server's request id, or "" when absent.
	RequestID string `json:"-"`
}

// Client calls the TypeSafe AI API. Construct one with [NewClient]; a Client
// is safe for concurrent use once constructed.
type Client struct {
	baseURL          string
	apiKey           string
	model            string
	httpClient       *http.Client
	timeout          time.Duration
	headers          map[string]string
	retry            RetryPolicy
	maxResponseBytes int64
	logger           *slog.Logger
	now              func() time.Time
	sleep            func(context.Context, time.Duration) error
}

// Option configures a Client.
type Option func(*Client) error

// WithAPIKey supplies the API key explicitly instead of reading [APIKeyEnv].
// Leading and trailing whitespace is stripped. An empty key, or one
// containing whitespace, control characters, or non-ASCII characters, is
// rejected; the key's value never appears in the error.
func WithAPIKey(key string) Option {
	return func(c *Client) error {
		key, err := validateAPIKey(key)
		if err != nil {
			return err
		}
		c.apiKey = key
		return nil
	}
}

// validateAPIKey strips surrounding whitespace and checks that what remains
// is a non-empty run of printable ASCII without spaces, so an invalid key is
// reported at construction rather than as a failed request. The returned
// error never contains the key.
func validateAPIKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("api key must not be empty")
	}
	for i := 0; i < len(key); i++ {
		if b := key[i]; b <= ' ' || b >= 0x7f {
			return "", errors.New("api key must contain only printable ASCII characters without whitespace")
		}
	}
	return key, nil
}

// WithModel sets the default model for requests that leave Model empty,
// instead of reading [DefaultModelEnv]. The built-in default is
// [ModelJevLatest].
func WithModel(model string) Option {
	return func(c *Client) error {
		if strings.TrimSpace(model) == "" {
			return errors.New("model must not be empty")
		}
		c.model = model
		return nil
	}
}

// WithBaseURL overrides the API root instead of reading [BaseURLEnv]. The URL
// must be absolute with an http or https scheme; a trailing slash is
// removed.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) error {
		u, err := url.Parse(baseURL)
		if err != nil {
			return fmt.Errorf("base url: %w", err)
		}
		if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("base url %q must be an absolute http(s) URL", baseURL)
		}
		c.baseURL = strings.TrimRight(baseURL, "/")
		return nil
	}
}

// WithHTTPClient supplies the HTTP client, including any timeout or
// transport. Its Timeout is used as-is unless [WithTimeout] is also given.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) error {
		if httpClient == nil {
			return errors.New("http client must not be nil")
		}
		c.httpClient = httpClient
		return nil
	}
}

// WithTimeout sets the per-attempt HTTP timeout. The default is
// [DefaultTimeout]. Use the context passed to Ask to bound a whole call.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) error {
		if timeout <= 0 {
			return errors.New("timeout must be positive")
		}
		c.timeout = timeout
		return nil
	}
}

// WithHeaders adds default request headers to every call. Protected headers
// (authentication, SDK identification, Accept) are not overridable.
func WithHeaders(headers map[string]string) Option {
	return func(c *Client) error {
		c.headers = headers
		return nil
	}
}

// WithRetryPolicy overrides the retry policy. Pass RetryPolicy{} to disable
// retries.
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
// The default is 16 MiB.
func WithMaxResponseBytes(limit int64) Option {
	return func(c *Client) error {
		if limit <= 0 {
			return errors.New("max response bytes must be positive")
		}
		c.maxResponseBytes = limit
		return nil
	}
}

// WithLogger logs each attempt at Info level (method, path, status,
// duration, request id) and retries at Warn level. Headers and bodies are
// never logged, so the API key and the state cannot leak through the log.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) error {
		c.logger = logger
		return nil
	}
}

// NewClient constructs a Client. Explicit options take precedence over the
// environment variables; the API key is required from one or the other.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		retry:            DefaultRetryPolicy(),
		maxResponseBytes: defaultMaxResponseBytes,
		now:              time.Now,
		sleep:            sleepContext,
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if c.apiKey == "" {
		fromEnv := envValue(APIKeyEnv)
		if fromEnv == "" {
			return nil, fmt.Errorf("api key is required: pass WithAPIKey or set %s", APIKeyEnv)
		}
		key, err := validateAPIKey(fromEnv)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", APIKeyEnv, err)
		}
		c.apiKey = key
	}
	if c.baseURL == "" {
		if fromEnv := envValue(BaseURLEnv); fromEnv != "" {
			if err := WithBaseURL(fromEnv)(c); err != nil {
				return nil, fmt.Errorf("%s: %w", BaseURLEnv, err)
			}
		} else {
			c.baseURL = DefaultBaseURL
		}
	}
	c.model = cmp.Or(c.model, envValue(DefaultModelEnv), ModelJevLatest)
	switch {
	case c.httpClient == nil:
		c.httpClient = &http.Client{Timeout: cmp.Or(c.timeout, DefaultTimeout)}
	case c.timeout != 0:
		// Copy so the caller's client is not mutated.
		clone := *c.httpClient
		clone.Timeout = c.timeout
		c.httpClient = &clone
	}
	return c, nil
}

func envValue(name string) string { return strings.TrimSpace(os.Getenv(name)) }

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Ask sends req and returns its validated answers. Transient failures are
// retried per the policy while ctx is live; the returned error is the last
// attempt's. Every non-2xx status surfaces as an *APIError, a failure to get
// a response as a *ConnectionError, and a 2xx body that does not match the
// questions as sent as ErrResponseValidation.
func (c *Client) Ask(ctx context.Context, req Request) (*Response, error) {
	raw, requestID, err := c.AskRaw(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := decodeResponse(raw, req.Questions)
	if err != nil {
		return nil, err
	}
	resp.RequestID = requestID
	return resp, nil
}

// AskRaw sends req exactly as Ask does but returns the 2xx body undecoded,
// together with the request id, for callers that model the response
// themselves (for example to read fields this SDK does not know about).
func (c *Client) AskRaw(ctx context.Context, req Request) (body []byte, requestID string, err error) {
	req.Model = cmp.Or(req.Model, c.model)
	if err := req.Validate(); err != nil {
		return nil, "", err
	}
	payload, err := req.body()
	if err != nil {
		return nil, "", fmt.Errorf("%w: encoding: %w", ErrInvalidRequest, err)
	}
	policy := c.retry
	if req.Retry != nil {
		policy = *req.Retry
	}
	return c.send(ctx, http.MethodPost, SystemOnePath, payload, req.Headers, policy)
}

// ListModels returns the models and aliases available to the account.
func (c *Client) ListModels(ctx context.Context) (*ModelList, error) {
	raw, requestID, err := c.send(ctx, http.MethodGet, ModelsPath, nil, nil, c.retry)
	if err != nil {
		return nil, err
	}
	var list ModelList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, validationError("decoding model list: %w", err)
	}
	for i, m := range list.Models {
		if m.Name == "" {
			return nil, validationError("models[%d].name is missing", i)
		}
	}
	list.RequestID = requestID
	return &list, nil
}

// send performs the retry loop around one request.
func (c *Client) send(ctx context.Context, method, path string, payload []byte, headers map[string]string, policy RetryPolicy) ([]byte, string, error) {
	endpoint := method + " " + path
	// Transports may echo header values into their errors; mask every
	// credential we send before an error reaches logs or callers.
	redact := newRedactor(c.apiKey, c.headers, headers)
	started := c.now()
	for attempt := 0; ; attempt++ {
		body, requestID, err := c.do(ctx, method, path, payload, headers, attempt, redact)
		if err == nil {
			return body, requestID, nil
		}
		if attempt >= policy.MaxRetries || ctx.Err() != nil || !policy.retryable(err) {
			return nil, "", err
		}
		var retryAfter time.Duration
		if apiErr, ok := errors.AsType[*APIError](err); ok {
			retryAfter = apiErr.RetryAfter
		}
		delay := policy.delay(attempt+1, retryAfter)
		if policy.Budget > 0 && c.now().Sub(started)+delay >= policy.Budget {
			return nil, "", err
		}
		if policy.OnRetry != nil {
			policy.OnRetry(attempt+1, err, delay)
		}
		if c.logger != nil {
			c.logger.WarnContext(ctx, "jev: retrying", "endpoint", endpoint, "attempt", attempt+1, "delay", delay, "error", err.Error())
		}
		if sleepErr := c.sleep(ctx, delay); sleepErr != nil {
			return nil, "", fmt.Errorf("jev: retry interrupted: %w (last error: %w)", sleepErr, err)
		}
	}
}

// do performs one HTTP attempt and returns the 2xx body and request id.
// Transport errors pass through redact before they are returned or logged.
func (c *Client) do(ctx context.Context, method, path string, payload []byte, headers map[string]string, attempt int, redact redactor) ([]byte, string, error) {
	endpoint := method + " " + path
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, "", fmt.Errorf("jev: building request: %w", err)
	}
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	for _, k := range protectedHeaders {
		httpReq.Header.Del(k)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", sdkIdentity)
	httpReq.Header.Set("X-Typesafe-Sdk", sdkIdentity)
	httpReq.Header.Set("X-Typesafe-Runtime", runtimeIdentity)
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if attempt > 0 {
		httpReq.Header.Set("X-Typesafe-Retry-Count", strconv.Itoa(attempt))
	}

	started := c.now()
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if isContextError(err) && ctx.Err() != nil {
			return nil, "", fmt.Errorf("jev: %s: %w", endpoint, ctx.Err())
		}
		connErr := newConnectionError(endpoint, err, redact)
		if c.logger != nil {
			c.logger.InfoContext(ctx, "jev: request failed", "endpoint", endpoint, "error", connErr.Error())
		}
		return nil, "", connErr
	}
	defer func() { _ = httpResp.Body.Close() }()
	requestID := httpResp.Header.Get(RequestIDHeader)
	if c.logger != nil {
		c.logger.InfoContext(ctx, "jev: request", "endpoint", endpoint, "status", httpResp.StatusCode,
			"duration", c.now().Sub(started), "request_id", requestID)
	}

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, c.maxResponseBytes+1))
	if err != nil {
		if isContextError(err) && ctx.Err() != nil {
			return nil, "", fmt.Errorf("jev: %s: %w", endpoint, ctx.Err())
		}
		return nil, "", newConnectionError(endpoint, err, redact)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return nil, "", fmt.Errorf("%w: more than %d bytes", ErrResponseTooLarge, c.maxResponseBytes)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", &APIError{
			StatusCode: httpResp.StatusCode,
			Message:    extractMessage(raw),
			Body:       raw,
			Endpoint:   endpoint,
			RequestID:  requestID,
			RetryAfter: parseRetryAfter(httpResp.Header, c.now()),
		}
	}
	return raw, requestID, nil
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
