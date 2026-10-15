# jev-go-sdk

A dependency-free Go client for [TypeSafe AI](https://typesafe.ai)'s API and
its flagship System One model, **Jev**. It mirrors the feature surface of the
official Python and JavaScript SDKs.

Jev is not a chat model. You send it a *state* (text, JSON, or images) and a set of
*typed questions*; it returns one calibrated, probability-bearing answer per
question in a few hundred milliseconds. No generated text, no tool loop, no
prompt parsing: the answers are values your code can branch on directly.

```go
import "github.com/ajayk/jev-go-sdk"
```

Requires Go 1.26 or newer. Only the standard library is used.

## Quick start

Set `TYPESAFE_API_KEY`, then:

```go
client, err := jev.NewClient()
if err != nil {
    log.Fatal(err)
}

resp, err := client.Ask(ctx, jev.Request{
    State: "I was charged twice for my order. Please fix this ASAP.",
    Questions: map[string]jev.Question{
        "refund": jev.Noul{Instructions: "Is the customer asking for money back?"},
        "route": jev.Choice{
            Instructions: "Which team should handle this?",
            Options: map[string]jev.Content{
                "billing": "payments, charges, refunds",
                "support": "product issues and how-to questions",
                "sales":   nil, // a nil description is interpreted by its name alone
            },
        },
        "urgency": jev.Score{
            Instructions: "How urgent is this message?",
            Levels:       []jev.Content{"low", "medium", "high"},
        },
    },
})
if err != nil {
    log.Fatal(err)
}

refund, _ := resp.Noul("refund")    // refund.Probability in [0, 1]
route, _ := resp.Choice("route")    // route.Choice, route.Probabilities, route.Confidence
urgency, _ := resp.Score("urgency") // urgency.Score, urgency.Level(), urgency.Legend
```

Every question in a request is evaluated independently against the same state,
so ask several at once rather than chaining calls. `resp.Nouls()`,
`resp.Choices()`, `resp.Scores()`, and `resp.Boxes()` return the answers
grouped by kind.

## Question types

| Type | Ask | Get back |
| --- | --- | --- |
| `jev.Noul` | a yes/no question or statement, with optional descriptions of what yes and no mean (instructions, a description, or both are required) | `NoulAnswer{Probability}`: the probability of yes |
| `jev.Choice` | pick one label from a described set (up to 255) | `ChoiceAnswer{Choice, Probabilities, Confidence}` |
| `jev.Score` | rate the state on an ordered rubric, lowest level first | `ScoreAnswer{Score, Probabilities, Legend, Confidence}`; `Level()` gives the most probable level index |
| `jev.Box` | locate what the instructions describe in the request's image | `BoxAnswer{Box, Confidence}`: the box's `Top`, `Bottom`, `Left`, `Right` edges as fractions of the image, and a confidence (nil when the model gives none) |
| `jev.Raw` | a question as a plain JSON object with a `"type"` key, for dynamically built questions | the answer for whichever type it names |

`Instructions`, option descriptions, rubric levels, and `State` accept a Go
string, an image, or any value that marshals to a JSON object or array
(`jev.Content`). NaN and infinite floats inside maps, slices, and arrays are
sent as the strings `"NaN"`, `"Infinity"`, and `"-Infinity"`, since JSON has no
tokens for them.

## Images

Image assets need a vision-language model. Build an image from bytes, a file,
base64 text, or an `image.Image`:

```go
photo := jev.ImageFromFile("photos/product.png") // read once, on first use
scan := jev.NewImage(data)                        // image file bytes
upload, err := jev.ImageFromBase64(text)          // base64 text
chart, err := jev.EncodeImage(img, "png")         // an image.Image, as "jpeg" or "png"
```

Then send it in any of three ways:

```go
// 1. Place it directly in content: the state, instructions, or criteria.
resp, err := client.Ask(ctx, jev.Request{
    State:     photo,
    Questions: map[string]jev.Question{"dog": jev.Box{Instructions: "The dog"}},
    Model:     jev.ModelJevPreview,
})

// 2. Refer to it by an alias in text, listing it in Assets.
resp, err := client.Ask(ctx, jev.Request{
    State:     "Product photo: <photo>",
    Assets:    map[string]*jev.Image{"<photo>": photo},
    Questions: map[string]jev.Question{"damaged": jev.Noul{Instructions: "Is the product damaged?"}},
})

// 3. Refer to it inside text through its String form, while attached.
detach := client.AttachAssets(photo)
defer detach()
resp, err := client.Ask(ctx, jev.Request{
    State:     fmt.Sprintf("Product photo: %s", photo),
    Questions: map[string]jev.Question{"damaged": jev.Noul{Instructions: "Is the product damaged?"}},
})
```

A reference to an image that is neither placed, listed in `Assets`, nor
attached is sent as plain text, without the image. Requests that carry images
are sent as `multipart/form-data`, with the JSON body as the first part and
each image's raw bytes as its own part. An `*Image` is read once and reused by
every request that sends it.

## Configuration

Options win over environment variables; blank environment values are ignored.

| Option | Environment variable | Default |
| --- | --- | --- |
| `WithAPIKey(key)` | `TYPESAFE_API_KEY` | required; surrounding whitespace is stripped, and the key must be printable ASCII without spaces |
| `WithBaseURL(url)` | `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` |
| `WithModel(id)` | `TYPESAFE_DEFAULT_MODEL` | `jev-latest` |
| `WithTimeout(d)` | | 10 s per attempt |
| `WithHTTPClient(c)` | | `http.Client` with the timeout above |
| `WithHeaders(map)` | | none |
| `WithRetryPolicy(p)` | | `jev.DefaultRetryPolicy()` |
| `WithMaxResponseBytes(n)` | | 16 MiB |
| `WithLogger(*slog.Logger)` | | no logging |
| `WithCompression(encoding, fn)` | | bodies sent uncompressed |

Per call, `Request` carries `Model`, `Assets`, `Headers`, `Extra` (additional top-level
body fields for API features this SDK does not model yet), and `Retry`. Bound a
whole call, including retries, with the context you pass to `Ask`.

`client.ListModels(ctx)` returns the models and aliases available to the
account. `client.AskRaw(ctx, req)` returns the undecoded 2xx body for callers
who model the response themselves.

## Request compression

The official SDKs compress request bodies of 1 KiB or more with zstd by
default. The Go standard library has no zstd, so this SDK sends bodies
uncompressed unless you plug in a compressor, for example
[klauspost/compress](https://github.com/klauspost/compress):

```go
encoder, _ := zstd.NewWriter(nil) // safe for concurrent EncodeAll
client, err := jev.NewClient(jev.WithCompression("zstd", func(body []byte) ([]byte, error) {
    return encoder.EncodeAll(body, nil), nil
}))
```

The body is compressed once per call and reused by every retry. Compression
helps most with large states and images. Other APIs, such as some gateways,
may not accept compressed bodies yet.

## Performance

For high-concurrency use, share one `Client` across goroutines; it is safe for
concurrent use and pools connections. The default client keeps up to 100 idle
connections per host alive for 30 seconds, so bursts of concurrent calls reuse
connections instead of opening new ones with fresh TLS handshakes
(`http.DefaultTransport` keeps only 2 per host). If you supply your own
transport, raise `MaxIdleConnsPerHost` to match your concurrency. Ask several
questions in one request rather than one request per question, and consider
HTTP/2 and compression, below and above.

## HTTP/2

HTTP/2 multiplexes many concurrent requests over one connection, which helps
when you fan out calls from several goroutines. Unlike the Python SDK, no extra
dependency is needed: the default client's transport is cloned from
`http.DefaultTransport`, which negotiates HTTP/2 over TLS automatically. If you supply your own transport,
keep HTTP/2 enabled:

```go
transport := http.DefaultTransport.(*http.Transport).Clone()
transport.MaxIdleConnsPerHost = 32 // your tuning here
client, err := jev.NewClient(
    jev.WithHTTPClient(&http.Client{Transport: transport, Timeout: jev.DefaultTimeout}),
)
```

A hand-built `&http.Transport{...}` with a custom `TLSClientConfig` or
`DialTLSContext` does not attempt HTTP/2 unless you set `ForceAttemptHTTP2`
(or `Protocols`).

## AI gateways

Point `WithBaseURL` (or `TYPESAFE_BASE_URL`) at any service that implements
the TypeSafe OpenAPI specification; the client appends `/v1/systemone` and
`/v1/models` to it. Use the gateway's own key and model id.

```go
// OpenRouter
client, err := jev.NewClient(
    jev.WithAPIKey(os.Getenv("OPENROUTER_API_KEY")),
    jev.WithBaseURL("https://openrouter.ai/api"),
    jev.WithModel("~typesafe/jev-latest"),
)

// Vercel AI Gateway
client, err := jev.NewClient(
    jev.WithAPIKey(os.Getenv("AI_GATEWAY_API_KEY")),
    jev.WithBaseURL("https://ai-gateway.vercel.sh/typesafe"),
    jev.WithModel("typesafe-ai/jev"),
)
```

Gateway-specific headers go in `WithHeaders` (for every call) or
`Request.Headers` (per call). Header values whose names look like credentials
are masked if a transport echoes them into an error; see Privacy.

## Errors

- Requests are validated before sending; problems wrap `jev.ErrInvalidRequest`.
- Every non-2xx status is a `*jev.APIError` with `StatusCode`, a `Message`
  extracted from the JSON body (`error`, `message`, or `detail`, including
  validation lists), `Endpoint`, `RequestID`, `RetryAfter`, and the raw `Body`.
  It also matches a status-class sentinel through `errors.Is`:
  `ErrBadRequest`, `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`,
  `ErrUnprocessable`, `ErrRateLimited`, or `ErrServer` (5xx, including 529).
- A request that gets no HTTP response is a `*jev.ConnectionError`, matching
  `jev.ErrConnection`, and `jev.ErrTimeout` when the HTTP timeout elapsed.
- A 2xx body is checked against the questions you sent. A missing answer, an
  answer for a question you did not ask, a mismatched kind, an undeclared
  label, an incomplete or non-normalised distribution, or an out-of-range
  probability, score, or confidence wraps `jev.ErrResponseValidation`. The
  client fails closed rather than handing you partial data.
- Bodies above the cap wrap `jev.ErrResponseTooLarge`.

## Retries

`jev.DefaultRetryPolicy()` matches the official SDKs: two retries starting at
500 ms with a 5 s cap and 25% jitter, on 408, 429, every 5xx, connection
errors, and timeouts, honouring `Retry-After` and `retry-after-ms`, within a
30 s total budget. Retrying stops as soon as your context is done. Start from
the default and adjust:

```go
policy := jev.DefaultRetryPolicy()
policy.MaxRetries = 4
policy.Statuses = []int{429, 502, 503, 504}
policy.OnRetry = func(attempt int, err error, delay time.Duration) {
    log.Printf("jev retry %d in %v: %v", attempt, delay, err)
}
client, _ := jev.NewClient(jev.WithRetryPolicy(policy))
```

Pass `jev.RetryPolicy{}` to disable retries.

## Privacy

The API key is validated when the client is built and never written to logs
or errors; the construction error for a malformed key does not contain the
key. `WithLogger` records method, path, status, duration, and request id only,
never headers or bodies. Transports and proxies can quote header values in
their errors, so a `*ConnectionError` masks the API key and the values of
credential-bearing headers (`Authorization`, `Proxy-Authorization`,
`X-API-Key`, `Api-Key`, `Cookie`, and any name containing `token` or `secret`)
as `***`, including in the unwrapped chain and in what `OnRetry` and the
logger see. Whatever you put in `State` is sent verbatim to a third-party API;
redact secrets before calling.

## API limits (as documented by TypeSafe)

- State is a string, a JSON object, or a JSON array, which can reference
  images. Images need a vision-language model; no audio.
- About 64k tokens per request, with state plus the longest single question
  within about 32k tokens.
- Input tokens are billed; output is free.

See the [TypeSafe docs](https://docs.typesafe.ai), the
[Python SDK](https://github.com/typesafe-ai/typesafe-sdk-python) this client
mirrors, and the
[System One launch post](https://typesafe.ai/blog/introducing-system-one-models-and-jev).

## Status

This is a community client, not an official TypeSafe AI SDK. Response
validation is deliberately strict; if the live API's answer shape differs from
the documented contract, please open an issue with the (redacted) response.

## License

Apache-2.0. See [LICENSE](LICENSE).
