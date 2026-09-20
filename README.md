# jev-go-sdk

A dependency-free Go client for [TypeSafe AI](https://typesafe.ai)'s System One
API and its flagship model, **Jev**.

Jev is not a chat model. You send it a *state* (text or JSON) and a set of
*typed questions*; it returns one calibrated, probability-bearing answer per
question in a few hundred milliseconds. No generated text, no tool loop, no
prompt parsing: the answers are values your code can branch on directly.

```go
import "github.com/ajayk/jev-go-sdk"
```

Requires Go 1.26 or newer. Only the standard library is used.

## Quick start

```go
client, err := jev.NewClient() // reads TYPESAFE_API_KEY
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
                "sales":   "new purchases and upgrades",
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
so ask several at once rather than chaining calls.

## Question types

| Type | Ask | Get back |
| --- | --- | --- |
| `jev.Noul` | a yes/no question, with optional descriptions of what yes and no mean | `NoulAnswer{Probability}`: the probability of yes |
| `jev.Choice` | pick one label from a described set (two or more labels) | `ChoiceAnswer{Choice, Probabilities, Confidence}` |
| `jev.Score` | rate the state on an ordered rubric (two or more levels, lowest first) | `ScoreAnswer{Score, Probabilities, Legend, Confidence}`; `Level()` gives the most probable level index |

`Instructions`, option descriptions, and rubric levels accept a Go string or any
value that marshals to a JSON object or array (`jev.Content`). The same goes
for `State`.

## Errors

- Requests are validated before they are sent; problems wrap `jev.ErrInvalidRequest`.
- Every non-2xx status is a `*jev.APIError` with `StatusCode`, a bounded
  control-character-free `Body` excerpt, and any `RetryAfter` hint. The API
  documents 401, 422, 429, and 529 (`jev.StatusOverloaded`).
- A 2xx body is checked against the questions you sent. A missing answer, an
  answer for a question you did not ask, a mismatched kind, an undeclared
  label, an incomplete distribution, or an out-of-range probability, score, or
  confidence wraps `jev.ErrResponseValidation`. The client fails closed rather
  than handing you partial data.
- Bodies above the cap (16 MiB by default) wrap `jev.ErrResponseTooLarge`.
- `jev.IsRetryable(err)` reports whether an error is transient.

## Retries

Transient failures (408, 429, 5xx, 529, and HTTP client or transport timeouts)
are retried with exponential backoff and jitter, honouring a `Retry-After`
header when it is longer than the computed delay. The default policy makes two
retries starting at 500 ms. Retrying stops as soon as your context is done.

```go
client, _ := jev.NewClient(jev.WithRetryPolicy(jev.RetryPolicy{
    MaxRetries: 4,
    BaseDelay:  250 * time.Millisecond,
    MaxDelay:   3 * time.Second,
    MaxJitter:  100 * time.Millisecond,
    OnRetry: func(attempt int, err error, delay time.Duration) {
        log.Printf("jev retry %d after %v: %v", attempt, delay, err)
    },
}))
```

## Configuration

| Option | Default |
| --- | --- |
| `WithAPIKey(key)` | `$TYPESAFE_API_KEY` |
| `WithModel(id)` | `jev-latest` (`jev.ModelJevLatest`; `jev.ModelJevPreview` is also published) |
| `WithEndpoint(url)` | `https://api.typesafe.ai/v1/systemone` |
| `WithHTTPClient(c)` | `&http.Client{Timeout: 30 * time.Second}` |
| `WithRetryPolicy(p)` | `jev.DefaultRetryPolicy()` |
| `WithMaxResponseBytes(n)` | 16 MiB |

The API key is never written to logs or errors. Whatever you put in `State`
is sent verbatim to a third-party API; redact secrets before calling.

## API limits (as documented by TypeSafe)

- Text only: a string, a JSON object, or a JSON array. No images or audio.
- About 64k tokens per request, with state plus the longest single question
  within about 32k tokens.
- A `Choice` supports up to 255 options. Input tokens are billed; output is
  free.

See the [TypeSafe docs](https://docs.typesafe.ai) and the
[System One launch post](https://typesafe.ai/blog/introducing-system-one-models-and-jev).

## Status

This is a community client, not an official TypeSafe AI SDK. The response
validation is deliberately strict; if the live API's answer shape differs from
the documented contract, please open an issue with the (redacted) response.

## License

Apache-2.0. See [LICENSE](LICENSE).
