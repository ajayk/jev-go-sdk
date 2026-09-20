// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

// Package jev is a dependency-free Go client for TypeSafe AI's System One API
// and its flagship model, Jev.
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
// Questions in one request are evaluated independently against the same
// state, so one answer never conditions another. Every response is checked
// against the questions as sent: a missing answer, a mismatched kind, an
// undeclared label, an incomplete distribution, or an out-of-range value is
// reported as [ErrResponseValidation] rather than silently accepted.
//
// The client never logs the API key or request bodies. What you send is
// forwarded verbatim to a third-party API; redact before you call.
package jev
