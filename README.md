# jev-go-sdk

A dependency-free Go client for [TypeSafe AI](https://typesafe.ai)'s API and
its flagship System One model, **Jev**. It mirrors the feature surface of the
official Python and JavaScript SDKs.

Jev is not a chat model. You send it a *state* (text or JSON) and a set of
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
`resp.Choices()`, and `resp.Scores()` return the answers grouped by kind.

## Question types

| Type | Ask | Get back |
| --- | --- | --- |
| `jev.Noul` | a yes/no question or statement, with optional descriptions of what yes and no mean | `NoulAnswer{Probability}`: the probability of yes |
| `jev.Choice` | pick one label from a described set (up to 255) | `ChoiceAnswer{Choice, Probabilities, Confidence}` |
| `jev.Score` | rate the state on an ordered rubric, lowest level first | `ScoreAnswer{Score, Probabilities, Legend, Confidence}`; `Level()` gives the most probable level index |
| `jev.Raw` | a question as a plain JSON object with a `"type"` key, for dynamically built questions | the answer for whichever type it names |

`Instructions`, option descriptions, rubric levels, and `State` accept a Go
string or any value that marshals to a JSON object or array (`jev.Content`).

## Configuration

Options win over environment variables; blank environment values are ignored.

| Option | Environment variable | Default |
| --- | --- | --- |
| `WithAPIKey(key)` | `TYPESAFE_API_KEY` | required |
| `WithBaseURL(url)` | `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` |
| `WithModel(id)` | `TYPESAFE_DEFAULT_MODEL` | `jev-latest` |
| `WithTimeout(d)` | | 10 s per attempt |
| `WithHTTPClient(c)` | | `http.Client` with the timeout above |
| `WithHeaders(map)` | | none |
| `WithRetryPolicy(p)` | | `jev.DefaultRetryPolicy()` |
| `WithMaxResponseBytes(n)` | | 16 MiB |
| `WithLogger(*slog.Logger)` | | no logging |

Per call, `Request` carries `Model`, `Headers`, `Extra` (additional top-level
body fields for API features this SDK does not model yet), and `Retry`. Bound a
whole call, including retries, with the context you pass to `Ask`.

`client.ListModels(ctx)` returns the models and aliases available to the
account. `client.AskRaw(ctx, req)` returns the undecoded 2xx body for callers
who model the response themselves.

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

The API key is never written to logs or errors, and `WithLogger` records
method, path, status, duration, and request id only, never headers or bodies.
Whatever you put in `State` is sent verbatim to a third-party API; redact
secrets before calling.

## API limits (as documented by TypeSafe)

- Text only: a string, a JSON object, or a JSON array. No images or audio.
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
