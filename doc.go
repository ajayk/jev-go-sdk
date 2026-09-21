// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

// Package jev is a dependency-free Go client for TypeSafe AI's API and its
// flagship System One model, Jev.
//
// A System One call sends one state (text or JSON) together with a map of
// typed questions, and receives one calibrated, probability-bearing answer per
// question. There is no generated text, no conversation, and no tool calling:
// the request is a function call and the answers are values your code can
// branch on directly.
//
// Three question primitives exist:
//
//   - [Noul] asks a yes/no question and yields the probability of yes.
//   - [Choice] picks one label from a described set and yields the label, a
//     probability per label, and a confidence.
//   - [Score] rates the state on an ordered rubric and yields the weighted
//     position, a probability per level, and a confidence.
//
// [Raw] carries a question as a plain JSON object for callers that build
// questions dynamically.
//
// Questions in one request are evaluated independently against the same
// state, so one answer never conditions another. Every response is checked
// against the questions as sent: a missing answer, a mismatched kind, an
// undeclared label, an incomplete distribution, or an out-of-range value is
// reported as [ErrResponseValidation] rather than silently accepted.
//
// Configuration follows the official SDKs: the API key, base URL, and default
// model come from options or from the TYPESAFE_API_KEY, TYPESAFE_BASE_URL, and
// TYPESAFE_DEFAULT_MODEL environment variables. The API key is validated when
// the client is built. The client never logs the API key or request bodies,
// and masks credentials that a transport echoes into a connection error. What
// you send is forwarded verbatim to a third-party API; redact before you call.
package jev
