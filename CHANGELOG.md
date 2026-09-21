# Changelog

## v0.2.1 (2026-09-21)

Tracks the official Python SDK v0.7.1.

### Bug fixes

- The API key is validated when the client is built, not when the first request fails. Leading and trailing whitespace is stripped from `WithAPIKey` and `TYPESAFE_API_KEY`; an empty key, or one containing whitespace, control characters, or non-ASCII characters, is rejected. An invalid explicit key does not fall back to the environment, and the error never contains the key.
- Credentials are masked in `*ConnectionError`. Transports (including custom `http.RoundTripper`s and proxies) can echo header values into their errors; the API key and the values of credential-bearing headers (`Authorization`, `Proxy-Authorization`, `X-API-Key`, `Api-Key`, `Cookie`, and any name containing `token` or `secret`) are replaced by `***` in the error, in whatever `WithLogger` and `OnRetry` see, and in the unwrapped chain. Errors free of credentials are untouched, so `errors.Is`/`errors.As` on the transport error keep working.
- Locally rejected requests (for example a header value containing a newline) surface as `*ConnectionError` and are retried like other connection errors, matching the official SDKs.

### Documentation

- Examples for using the client through AI gateways (OpenRouter, Vercel AI Gateway).

## v0.2.0 (2026-09-19)

Aligns the client with the official TypeSafe SDKs' feature surface.

### Breaking changes

- `WithEndpoint` is replaced by `WithBaseURL`; the client derives `/v1/systemone` and `/v1/models` from the API root.
- `RetryPolicy.MaxJitter` (a duration added to the delay) is replaced by `Jitter` (a fraction in [0, 1] subtracted from it), matching the official policy. Start from `DefaultRetryPolicy()` and adjust fields; a zero policy retries nothing.
- `ScoreAnswer.Legend` values are `Content` rather than `string`, since rubric levels may be structured.
- `ScoreAnswer.Level()` returns the level index as an `int` (-1 when empty).
- `Choice` accepts one or more options and `Score` one or more levels, matching the API schema. `Instructions` is optional on every question.
- Transport failures are `*ConnectionError` values (matching `ErrConnection`, and `ErrTimeout` for timeouts) instead of wrapped `*url.Error` values.
- The default per-attempt HTTP timeout is 10 seconds, matching the official SDKs.

### Added

- `Client.ListModels` for `GET /v1/models`.
- `Client.AskRaw` returning the undecoded 2xx body and request id.
- `Raw` questions given as plain JSON objects, converted to the typed question before sending.
- `Request.Extra` (extra top-level body fields), `Request.Headers` (per-call headers), and `Request.Retry` (per-call retry override).
- `WithModel`, `WithTimeout`, `WithHeaders`, and `WithLogger` options; `TYPESAFE_BASE_URL` and `TYPESAFE_DEFAULT_MODEL` environment variables alongside `TYPESAFE_API_KEY`.
- `APIError.Message` extracted from `error`, `message`, and `detail` bodies (including FastAPI validation lists), plus `Endpoint`, `RequestID`, and `Body`. `APIError` matches the status-class sentinels `ErrBadRequest`, `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`, `ErrUnprocessable`, `ErrRateLimited`, and `ErrServer` through `errors.Is`.
- `RetryPolicy.Statuses`, `IgnoreRetryAfter`, `ConnectionErrors`, `Timeouts`, `Predicate`, and `Budget`. `retry-after-ms` and HTTP-date `Retry-After` headers are honoured.
- `Response.RequestID` and grouped accessors `Nouls()`, `Choices()`, `Scores()`.
- `X-TypeSafe-SDK`, `X-TypeSafe-Runtime`, and `X-TypeSafe-Retry-Count` request headers.

## v0.1.0 (2026-09-19)

Initial client: typed questions, single-shot `Ask`, strict response validation, bounded reads, and basic retries.
