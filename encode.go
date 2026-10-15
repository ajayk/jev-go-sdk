// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"encoding/json"
	"math"
	"reflect"
)

var marshalerType = reflect.TypeFor[json.Marshaler]()

// maxFiniteDepth bounds the walk of stringifyNonFinite, so a value that
// contains itself is left for encoding/json to reject as a cycle.
const maxFiniteDepth = 1000

// stringifyNonFinite returns v with every NaN and infinite float replaced by
// the string "NaN", "Infinity", or "-Infinity", since JSON has no tokens for
// them and encoding/json rejects them. It descends through maps with string
// keys, slices, arrays, pointers, and interfaces, copying a container only
// when something inside it changes. Structs and json.Marshaler
// implementations are left as they are.
func stringifyNonFinite(v any) any {
	if v == nil {
		return nil
	}
	if out, changed := nonFinite(reflect.ValueOf(v), 0); changed {
		return out
	}
	return v
}

func nonFinite(v reflect.Value, depth int) (any, bool) {
	if depth > maxFiniteDepth || (v.Kind() != reflect.Interface && v.Type().Implements(marshalerType)) {
		return nil, false
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		switch f := v.Float(); {
		case math.IsNaN(f):
			return "NaN", true
		case math.IsInf(f, 1):
			return "Infinity", true
		case math.IsInf(f, -1):
			return "-Infinity", true
		}
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return nonFinite(v.Elem(), depth+1)
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		var out map[string]any
		for it := v.MapRange(); it.Next(); {
			replaced, changed := nonFinite(it.Value(), depth+1)
			if !changed {
				continue
			}
			if out == nil {
				out = make(map[string]any, v.Len())
				for all := v.MapRange(); all.Next(); {
					out[all.Key().String()] = all.Value().Interface()
				}
			}
			out[it.Key().String()] = replaced
		}
		return out, out != nil
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil, false // []byte encodes as base64.
		}
		var out []any
		for i := range v.Len() {
			replaced, changed := nonFinite(v.Index(i), depth+1)
			if !changed {
				continue
			}
			if out == nil {
				out = make([]any, v.Len())
				for j := range v.Len() {
					out[j] = v.Index(j).Interface()
				}
			}
			out[i] = replaced
		}
		return out, out != nil
	}
	return nil, false
}
