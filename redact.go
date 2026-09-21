// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"bytes"
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// redactionMask replaces a credential wherever it appears in an error.
const redactionMask = "***"

// secretHeaders are header names whose values are credentials. Names that
// contain "token" or "secret" are treated the same way.
var secretHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"x-api-key":           true,
	"api-key":             true,
	"cookie":              true,
	"set-cookie":          true,
}

func isSecretHeader(name string) bool {
	lowered := strings.ToLower(name)
	return secretHeaders[lowered] || strings.Contains(lowered, "token") || strings.Contains(lowered, "secret")
}

// redactor masks credentials in text. The zero value masks nothing.
type redactor struct {
	replacer *strings.Replacer
}

// newRedactor collects the API key and every credential-bearing header value
// from the given header maps. Transports echo header values in error messages
// in several spellings, so each credential is matched raw, Go-quoted (as %q
// prints it), and JSON-encoded.
func newRedactor(apiKey string, headerMaps ...map[string]string) redactor {
	credentials := map[string]bool{}
	add := func(value string) {
		if value != "" {
			credentials[value] = true
		}
	}
	add(apiKey)
	for _, headers := range headerMaps {
		for name, value := range headers {
			if !isSecretHeader(name) {
				continue
			}
			add(value)
			lowered := strings.ToLower(name)
			if lowered == "authorization" || lowered == "proxy-authorization" {
				// "Bearer <credential>": the credential alone may be echoed.
				if _, credential, ok := strings.Cut(strings.TrimSpace(value), " "); ok {
					add(strings.TrimSpace(credential))
				}
			}
		}
	}
	if len(credentials) == 0 {
		return redactor{}
	}
	variants := map[string]bool{}
	for value := range credentials {
		variants[value] = true
		quoted := strconv.Quote(value)
		variants[quoted[1:len(quoted)-1]] = true
		variants[jsonInner(value)] = true
	}
	// Longest first so a quoted spelling wins over its raw prefix.
	ordered := slices.Collect(maps.Keys(variants))
	slices.SortFunc(ordered, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	oldnew := make([]string, 0, 2*len(ordered))
	for _, v := range ordered {
		oldnew = append(oldnew, v, redactionMask)
	}
	return redactor{replacer: strings.NewReplacer(oldnew...)}
}

// jsonInner renders value as a JSON string without the surrounding quotes and
// without HTML escaping.
func jsonInner(value string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return value
	}
	s := strings.TrimSuffix(buf.String(), "\n")
	return s[1 : len(s)-1]
}

// contains reports whether s mentions any credential.
func (r redactor) contains(s string) bool {
	return r.replacer != nil && r.replacer.Replace(s) != s
}

// redact masks every credential in s.
func (r redactor) redact(s string) string {
	if r.replacer == nil {
		return s
	}
	return r.replacer.Replace(s)
}

// redactError returns err unchanged when its message is free of credentials.
// Otherwise it returns a flat error carrying only the masked message, so no
// unwrapping can reach the original text.
func (r redactor) redactError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !r.contains(msg) {
		return err
	}
	return &redactedError{msg: r.redact(msg)}
}

// redactedError is a transport error whose message contained a credential.
// It deliberately wraps nothing.
type redactedError struct {
	msg string
}

func (e *redactedError) Error() string { return e.msg }
