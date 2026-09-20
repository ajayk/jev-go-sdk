// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Content is the JSON content the API accepts for a state, an instruction, or
// a criteria description: a Go string, or any value that marshals to a JSON
// object or array. Other JSON kinds are rejected by the API with a 422.
type Content any

// Wire question kinds, also carried on every answer.
const (
	KindNoul   = "noul"
	KindChoice = "choice"
	KindScore  = "score"
)

// Question is one typed question. The implementations are [Noul], [Choice],
// and [Score], by value or by pointer; the interface is sealed so the response
// validator can match every answer to the exact question that produced it.
type Question interface {
	json.Marshaler
	// Kind returns the wire kind: "noul", "choice", or "score".
	Kind() string
	validate() error
}

type wireQuestion struct {
	Type         string  `json:"type"`
	Instructions Content `json:"instructions,omitempty"`
	Criteria     any     `json:"criteria,omitempty"`
}

// Noul asks a yes/no question. The answer is the probability of yes; there is
// no separate confidence because the probability already is one.
type Noul struct {
	// Instructions is the question to answer about the state.
	Instructions Content
	// True and False optionally describe what a yes and a no mean.
	True  Content
	False Content
}

var _ Question = (*Noul)(nil)

// Kind returns "noul".
func (Noul) Kind() string { return KindNoul }

func (q Noul) validate() error { return validateInstructions(q.Instructions) }

// MarshalJSON encodes the question in the wire shape.
func (q Noul) MarshalJSON() ([]byte, error) {
	w := wireQuestion{Type: KindNoul, Instructions: q.Instructions}
	if q.True != nil || q.False != nil {
		criteria := make(map[string]Content, 2)
		if q.True != nil {
			criteria["true"] = q.True
		}
		if q.False != nil {
			criteria["false"] = q.False
		}
		w.Criteria = criteria
	}
	return json.Marshal(w)
}

// Choice picks one label from a set of alternatives. The answer names the
// chosen label and carries a probability for every label.
type Choice struct {
	// Instructions describes what to decide about the state.
	Instructions Content
	// Options maps each selectable label to an optional description. A nil
	// description leaves the label undescribed. At least two labels are
	// required.
	Options map[string]Content
}

var _ Question = (*Choice)(nil)

// Kind returns "choice".
func (Choice) Kind() string { return KindChoice }

func (q Choice) validate() error {
	if err := validateInstructions(q.Instructions); err != nil {
		return err
	}
	if len(q.Options) < 2 {
		return errors.New("choice needs at least two options")
	}
	for label := range q.Options {
		if label == "" {
			return errors.New("choice option label must not be empty")
		}
	}
	return nil
}

// MarshalJSON encodes the question in the wire shape.
func (q Choice) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireQuestion{Type: KindChoice, Instructions: q.Instructions, Criteria: q.Options})
}

// Score rates the state on an ordered rubric. The answer is a weighted
// position on that rubric: level i is score i, so a two-level rubric yields a
// score in [0, 1] and a five-level rubric a score in [0, 4].
type Score struct {
	// Instructions describes what to rate about the state.
	Instructions Content
	// Levels is the rubric, lowest level first. At least two levels are
	// required.
	Levels []Content
}

var _ Question = (*Score)(nil)

// Kind returns "score".
func (Score) Kind() string { return KindScore }

func (q Score) validate() error {
	if err := validateInstructions(q.Instructions); err != nil {
		return err
	}
	if len(q.Levels) < 2 {
		return errors.New("score needs at least two levels")
	}
	for i, level := range q.Levels {
		if level == nil {
			return fmt.Errorf("score level %d must not be nil", i)
		}
	}
	return nil
}

// MarshalJSON encodes the question in the wire shape.
func (q Score) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireQuestion{Type: KindScore, Instructions: q.Instructions, Criteria: q.Levels})
}

func validateInstructions(instructions Content) error {
	if instructions == nil {
		return errors.New("instructions must not be nil")
	}
	return nil
}

// normalizeQuestion returns the value form of a question. Pointer forms
// satisfy Question through method-set promotion and are accepted; a nil
// pointer or a foreign implementation is rejected.
func normalizeQuestion(question Question) (Question, error) {
	switch q := question.(type) {
	case Noul, Choice, Score:
		return q, nil
	case *Noul:
		if q == nil {
			return nil, errors.New("question is a nil *Noul")
		}
		return *q, nil
	case *Choice:
		if q == nil {
			return nil, errors.New("question is a nil *Choice")
		}
		return *q, nil
	case *Score:
		if q == nil {
			return nil, errors.New("question is a nil *Score")
		}
		return *q, nil
	case nil:
		return nil, errors.New("question is nil")
	default:
		return nil, fmt.Errorf("unsupported question type %T", question)
	}
}
