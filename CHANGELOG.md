# Changelog

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
