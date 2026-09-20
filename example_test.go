// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/ajayk/jev-go-sdk"
)

// fakeAPI stands in for api.typesafe.ai so the example is deterministic. In
// real code, drop WithEndpoint and WithHTTPClient and set TYPESAFE_API_KEY.
func fakeAPI() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
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
		}`))
	}))
}

func Example() {
	srv := fakeAPI()
	defer srv.Close()

	client, err := jev.NewClient(
		jev.WithAPIKey("sk-example"),
		jev.WithEndpoint(srv.URL),
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
	fmt.Printf("urgency=%.1f most_likely=%s p=%.2f\n", urgency.Score, urgency.Legend[fmt.Sprint(level)], p)
	fmt.Printf("input_tokens=%d\n", resp.Usage.InputTokens)
	// Output:
	// refund p=0.93
	// route=billing confidence=0.85
	// urgency=1.7 most_likely=high p=0.75
	// input_tokens=61
}
