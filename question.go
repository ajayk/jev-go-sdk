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

// Question is one typed question: a [Noul], [Choice], or [Score] by value or
// pointer, or a [Raw] JSON object. The interface is sealed so the response
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
	// Instructions is the question or statement to evaluate. Optional.
	Instructions Content
	// True and False optionally describe what a yes and a no mean.
	True  Content
	False Content
}

var _ Question = (*Noul)(nil)

// Kind returns "noul".
func (Noul) Kind() string { return KindNoul }

func (Noul) validate() error { return nil }

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
	// Instructions describes what to decide about the state. Optional.
	Instructions Content
	// Options maps each selectable label to an optional description. A nil
	// description leaves the label interpreted by its name alone. At least
	// one label is required; the API supports up to 255.
	Options map[string]Content
}

var _ Question = (*Choice)(nil)

// Kind returns "choice".
func (Choice) Kind() string { return KindChoice }

func (q Choice) validate() error {
	if len(q.Options) == 0 {
		return errors.New("choice needs at least one option")
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
// position on that rubric: level i is score i, so a three-level rubric yields
// a score in [0, 2].
type Score struct {
	// Instructions describes what to rate about the state. Optional.
	Instructions Content
	// Levels is the rubric, lowest level first. At least one level is
	// required.
	Levels []Content
}

var _ Question = (*Score)(nil)

// Kind returns "score".
func (Score) Kind() string { return KindScore }

func (q Score) validate() error {
	if len(q.Levels) == 0 {
		return errors.New("score needs at least one level")
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

// Raw is a question given as its JSON object, for callers that build
// questions dynamically or load them from configuration. It must carry a
// "type" of "noul", "choice", or "score"; "choice" needs an object under
// "criteria" and "score" an array. It is converted to the matching typed
// question before sending, so answers are validated exactly as for one.
type Raw map[string]any

var _ Question = Raw(nil)

// Kind returns the value of the "type" key, or "" when absent.
func (q Raw) Kind() string {
	kind, _ := q["type"].(string)
	return kind
}

func (q Raw) validate() error {
	_, err := q.typed()
	return err
}

// MarshalJSON encodes the object as given.
func (q Raw) MarshalJSON() ([]byte, error) { return json.Marshal(map[string]any(q)) }

// typed converts the object into the typed question it describes.
func (q Raw) typed() (Question, error) {
	instructions := q["instructions"]
	switch q.Kind() {
	case KindNoul:
		n := Noul{Instructions: instructions}
		if criteria, ok := q["criteria"]; ok && criteria != nil {
			m, ok := criteria.(map[string]any)
			if !ok {
				return nil, errors.New(`noul "criteria" must be an object`)
			}
			n.True, n.False = m["true"], m["false"]
		}
		return n, nil
	case KindChoice:
		criteria, ok := q["criteria"].(map[string]any)
		if !ok {
			return nil, errors.New(`choice "criteria" must be an object of label to description`)
		}
		options := make(map[string]Content, len(criteria))
		for label, description := range criteria {
			options[label] = description
		}
		return Choice{Instructions: instructions, Options: options}, nil
	case KindScore:
		criteria, ok := q["criteria"].([]any)
		if !ok {
			if typed, ok := q["criteria"].([]Content); ok {
				return Score{Instructions: instructions, Levels: typed}, nil
			}
			if typed, ok := q["criteria"].([]string); ok {
				levels := make([]Content, len(typed))
				for i, s := range typed {
					levels[i] = s
				}
				return Score{Instructions: instructions, Levels: levels}, nil
			}
			return nil, errors.New(`score "criteria" must be an array of level descriptions`)
		}
		levels := make([]Content, len(criteria))
		for i, level := range criteria {
			levels[i] = level
		}
		return Score{Instructions: instructions, Levels: levels}, nil
	case "":
		return nil, errors.New(`raw question needs a nonempty string "type"`)
	default:
		return nil, fmt.Errorf("unsupported question type %q", q.Kind())
	}
}

// normalizeQuestion returns the typed value form of a question. Pointer forms
// satisfy Question through method-set promotion and are accepted; a Raw is
// converted; a nil pointer or a foreign implementation is rejected.
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
	case Raw:
		return q.typed()
	case nil:
		return nil, errors.New("question is nil")
	default:
		return nil, fmt.Errorf("unsupported question type %T", question)
	}
}
