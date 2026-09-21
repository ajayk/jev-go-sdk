// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/ajayk/jev-go-sdk"
)

// fakeAPI stands in for api.typesafe.ai so the examples are deterministic. In
// real code, drop WithBaseURL and WithHTTPClient and set TYPESAFE_API_KEY.
func fakeAPI(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}

func Example() {
	srv := fakeAPI(`{
		"model": "jev-1.13.0",
		"answers": {
			"refund": {"type": "noul", "noul": 0.93},
			"route": {"type": "choice", "choice": "billing",
				"probabilities": {"billing": 0.88, "support": 0.10, "sales": 0.02}, "confidence": 0.85},
			"urgency": {"type": "score", "score": 1.7,
				"legend": {"0": "low", "1": "medium", "2": "high"},
				"probabilities": {"0": 0.05, "1": 0.20, "2": 0.75}, "confidence": 0.8}
		},
		"usage": {"input_tokens": 61, "output_tokens": 3}
	}`)
	defer srv.Close()

	client, err := jev.NewClient(
		jev.WithAPIKey("sk-example"),
		jev.WithBaseURL(srv.URL),
		jev.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		panic(err)
	}

	resp, err := client.Ask(context.Background(), jev.Request{
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
		panic(err)
	}

	refund, _ := resp.Noul("refund")
	route, _ := resp.Choice("route")
	urgency, _ := resp.Score("urgency")
	level, p := urgency.Level()
	fmt.Printf("refund p=%.2f\n", refund.Probability)
	fmt.Printf("route=%s confidence=%.2f\n", route.Choice, route.Confidence)
	fmt.Printf("urgency=%.1f most_likely=%v p=%.2f\n", urgency.Score, urgency.Legend[fmt.Sprint(level)], p)
	fmt.Printf("input_tokens=%d\n", resp.Usage.InputTokens)
	// Output:
	// refund p=0.93
	// route=billing confidence=0.85
	// urgency=1.7 most_likely=high p=0.75
	// input_tokens=61
}

// Questions can be supplied as plain JSON objects, for example when they are
// loaded from configuration.
func ExampleRaw() {
	srv := fakeAPI(`{"model":"jev-1.13.0","answers":{"spam":{"type":"noul","noul":0.02}},"usage":{"input_tokens":9,"output_tokens":1}}`)
	defer srv.Close()
	client, _ := jev.NewClient(jev.WithAPIKey("sk-example"), jev.WithBaseURL(srv.URL), jev.WithHTTPClient(srv.Client()))

	resp, err := client.Ask(context.Background(), jev.Request{
		State: "Thanks for your help yesterday!",
		Questions: map[string]jev.Question{
			"spam": jev.Raw{"type": "noul", "instructions": "Is this message spam?"},
		},
	})
	if err != nil {
		panic(err)
	}
	spam, _ := resp.Noul("spam")
	fmt.Printf("spam p=%.2f\n", spam.Probability)
	// Output:
	// spam p=0.02
}

// Non-2xx responses are *APIError values that also match a status-class
// sentinel, so callers can branch with errors.Is.
func ExampleAPIError() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(jev.RequestIDHeader, "req-42")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":[{"loc":["body","state"],"msg":"Field required","type":"missing"}]}`))
	}))
	defer srv.Close()
	client, _ := jev.NewClient(jev.WithAPIKey("sk-example"), jev.WithBaseURL(srv.URL), jev.WithHTTPClient(srv.Client()))

	_, err := client.Ask(context.Background(), jev.Request{
		State:     "state",
		Questions: map[string]jev.Question{"q": jev.Noul{Instructions: "?"}},
	})
	var apiErr *jev.APIError
	fmt.Println(errors.Is(err, jev.ErrUnprocessable), errors.As(err, &apiErr))
	fmt.Println(apiErr.StatusCode, apiErr.RequestID, apiErr.Message)
	fmt.Println(err)
	// Output:
	// true true
	// 422 req-42 state: Field required
	// jev: POST /v1/systemone: HTTP 422 Unprocessable Entity: state: Field required (request_id=req-42)
}

func ExampleClient_ListModels() {
	srv := fakeAPI(`{"models":[{"name":"jev-latest","description":"General-purpose system one model.","release_date":"2026-09-15"}]}`)
	defer srv.Close()
	client, _ := jev.NewClient(jev.WithAPIKey("sk-example"), jev.WithBaseURL(srv.URL), jev.WithHTTPClient(srv.Client()))

	list, err := client.ListModels(context.Background())
	if err != nil {
		panic(err)
	}
	for _, m := range list.Models {
		fmt.Printf("%s released %s: %s\n", m.Name, m.ReleaseDate, m.Description)
	}
	// Output:
	// jev-latest released 2026-09-15: General-purpose system one model.
}

// Route through an AI gateway that implements the TypeSafe OpenAPI
// specification by pointing the client at the gateway's root and using the
// gateway's own key and model id. The client appends /v1/systemone itself.
func ExampleWithBaseURL() {
	// OpenRouter
	openRouter, err := jev.NewClient(
		jev.WithAPIKey(os.Getenv("OPENROUTER_API_KEY")),
		jev.WithBaseURL("https://openrouter.ai/api"),
		jev.WithModel("~typesafe/jev-latest"),
	)
	_, _ = openRouter, err

	// Vercel AI Gateway
	vercel, err := jev.NewClient(
		jev.WithAPIKey(os.Getenv("AI_GATEWAY_API_KEY")),
		jev.WithBaseURL("https://ai-gateway.vercel.sh/typesafe"),
		jev.WithModel("typesafe-ai/jev"),
	)
	_, _ = vercel, err
}
