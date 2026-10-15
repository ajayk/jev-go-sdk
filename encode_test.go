// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"testing"
)

func TestNonFiniteNumbersAreStringified(t *testing.T) {
	t.Parallel()
	var wire map[string]any
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&wire)
		_, _ = w.Write([]byte(sampleBody(`{"q": {"type": "noul", "noul": 0.5}, "r": {"type": "noul", "noul": 0.5}}`)))
	})
	nan, inf := math.NaN(), math.Inf(1)
	req := Request{
		State: map[string]any{"value": nan, "arr": []float64{1, inf, -inf}, "nested": map[string]float32{"x": float32(nan)}},
		Questions: map[string]Question{
			"q": Noul{Instructions: map[string]any{"threshold": inf}},
			"r": Raw{"type": "noul", "instructions": []any{nan}},
		},
		Extra: map[string]any{"temperature": nan},
	}
	if _, err := client.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	want := map[string]any{
		"model": "jev-latest",
		"state": map[string]any{"value": "NaN", "arr": []any{1.0, "Infinity", "-Infinity"}, "nested": map[string]any{"x": "NaN"}},
		"questions": map[string]any{
			"q": map[string]any{"type": "noul", "instructions": map[string]any{"threshold": "Infinity"}},
			"r": map[string]any{"type": "noul", "instructions": []any{"NaN"}},
		},
		"temperature": "NaN",
	}
	if !reflect.DeepEqual(wire, want) {
		t.Errorf("body = %v, want %v", wire, want)
	}
}

func TestStringifyNonFiniteLeavesFiniteValuesAlone(t *testing.T) {
	t.Parallel()
	state := map[string]any{"a": 1.5, "b": []any{"x", 2}, "c": []byte("raw")}
	got := stringifyNonFinite(state)
	if reflect.ValueOf(got).UnsafePointer() != reflect.ValueOf(state).UnsafePointer() {
		t.Error("stringifyNonFinite copied a map without non-finite values")
	}
	type withFloat struct{ F float64 }
	s := withFloat{F: math.NaN()}
	if got, ok := stringifyNonFinite(s).(withFloat); !ok || !math.IsNaN(got.F) {
		t.Errorf("stringifyNonFinite(struct) = %v, want it unchanged", got)
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if got := stringifyNonFinite(cyclic); reflect.ValueOf(got).UnsafePointer() != reflect.ValueOf(cyclic).UnsafePointer() {
		t.Error("stringifyNonFinite copied a cyclic map")
	}
}
