# Changelog

## v0.3.0 (2026-10-14)

Tracks the official Python SDK v0.7.5, including v0.7.3 and v0.7.4.

### Breaking changes

- A `Noul` (or raw `"noul"` question) with neither instructions nor a described outcome is rejected with `ErrInvalidRequest` before sending; set `Instructions`, `True`, or `False`.

### Features

- `Image` assets from bytes (`NewImage`), files (`ImageFromFile`), base64 text (`ImageFromBase64`), and `image.Image` values (`EncodeImage`, JPEG or PNG). Images can be placed directly in content, listed in the new `Request.Assets` under an alias, or referenced in text while attached with `Client.AttachAssets`. Requests that carry images are sent as `multipart/form-data`; each image is read once and reused.
- `Box` questions and `BoxAnswer` (box edges as fractions of the image, and an optional confidence), with `Response.Box` and `Response.Boxes`. Raw `"box"` questions are supported too.
- `WithCompression(encoding, fn)` compresses request bodies of 1 KiB or more. The Python SDK compresses with zstd by default; the Go standard library has no zstd, so this SDK stays dependency-free and leaves compression off unless you plug in a compressor such as `github.com/klauspost/compress/zstd`. `Content-Encoding` is now a protected header.

### Bug fixes

- NaN and infinite floats in the state, question content, raw questions, and `Request.Extra` are sent as the strings `"NaN"`, `"Infinity"`, and `"-Infinity"` instead of failing to encode. Maps, slices, and arrays are converted; structs are left to `encoding/json`.

### Performance

- The default HTTP client's transport is a clone of `http.DefaultTransport` that keeps up to 100 idle connections per host alive for 30 seconds (instead of 2 per host), so bursts of concurrent calls reuse connections rather than opening new TLS connections.

### Documentation

- Document images, `Box`, request compression, and a Performance section with tips for high-concurrency use.

## v0.2.2 (2026-09-27)

Tracks the official Python SDK v0.7.2.

The Python release adds an optional `http2` extra. Go needs no equivalent: the
default client already negotiates HTTP/2 over TLS through `http.DefaultTransport`.

### Documentation

- Document HTTP/2 use, including how to keep it enabled with a custom transport passed to `WithHTTPClient`.

### Tests

- Cover requests over HTTP/2 and guard that the default client keeps `http.DefaultTransport`.

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
