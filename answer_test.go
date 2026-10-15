// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func TestBoxRoundTrip(t *testing.T) {
	t.Parallel()
	var wire map[string]any
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&wire)
		_, _ = w.Write([]byte(sampleBody(`{
			"dog": {"type": "box", "box": {"top": 0.1, "bottom": 0.6, "left": 0.25, "right": 0.75}, "confidence": 0.9, "extra": {"k": [1.0]}},
			"cat": {"type": "box", "box": {"top": 0, "bottom": 1, "left": 0, "right": 1}, "confidence": null},
			"raw": {"type": "box", "box": {"top": 0.2, "bottom": 0.3, "left": 0.4, "right": 0.5}}
		}`)))
	})
	resp, err := client.Ask(t.Context(), Request{
		State:     "text",
		Questions: map[string]Question{"dog": Box{Instructions: "The dog"}, "cat": &Box{}, "raw": Raw{"type": "box", "instructions": "x"}},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	wantQuestions := map[string]any{
		"dog": map[string]any{"type": "box", "instructions": "The dog"},
		"cat": map[string]any{"type": "box"},
		"raw": map[string]any{"type": "box", "instructions": "x"},
	}
	if !reflect.DeepEqual(wire["questions"], wantQuestions) {
		t.Errorf("questions = %v, want %v", wire["questions"], wantQuestions)
	}
	dog, err := resp.Box("dog")
	if err != nil {
		t.Fatalf("Box(dog): %v", err)
	}
	if want := (BoxCoordinates{Top: 0.1, Bottom: 0.6, Left: 0.25, Right: 0.75}); dog.Box != want {
		t.Errorf("dog box = %+v, want %+v", dog.Box, want)
	}
	if dog.Confidence == nil || *dog.Confidence != 0.9 {
		t.Errorf("dog confidence = %v, want 0.9", dog.Confidence)
	}
	if cat, _ := resp.Box("cat"); cat.Confidence != nil {
		t.Errorf("cat confidence = %v, want nil", *cat.Confidence)
	}
	if got := len(resp.Boxes()); got != 3 {
		t.Errorf("len(Boxes()) = %d, want 3", got)
	}
	if len(resp.Nouls()) != 0 {
		t.Errorf("Nouls() = %v, want none", resp.Nouls())
	}
	if _, err := resp.Noul("dog"); !errors.Is(err, ErrNoAnswer) {
		t.Errorf("Noul(dog) error = %v, want ErrNoAnswer", err)
	}
}

func TestBoxAnswerValidation(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"missing box":         `{"type": "box", "confidence": 0.5}`,
		"missing edge":        `{"type": "box", "box": {"top": 0.1, "bottom": 0.6, "left": 0.25}, "confidence": 0.5}`,
		"edge out of range":   `{"type": "box", "box": {"top": -0.1, "bottom": 0.6, "left": 0.25, "right": 0.75}, "confidence": 0.5}`,
		"confidence too high": `{"type": "box", "box": {"top": 0.1, "bottom": 0.6, "left": 0.25, "right": 0.75}, "confidence": 1.5}`,
		"wrong kind":          `{"type": "noul", "noul": 0.5}`,
	}
	for name, answer := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeAnswer("b", json.RawMessage(answer), Box{})
			if !errors.Is(err, ErrResponseValidation) {
				t.Errorf("decodeAnswer(%s) error = %v, want ErrResponseValidation", answer, err)
			}
		})
	}
}
