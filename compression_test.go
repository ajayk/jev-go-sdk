// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// gzipCompress stands in for a zstd compressor, which the standard library
// lacks.
func gzipCompress(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(body); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func TestCompression(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	encodings := map[string]string{}
	var compressCalls atomic.Int32
	var attempts atomic.Int32
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		encoding := r.Header.Get("Content-Encoding")
		var body io.Reader = r.Body
		if encoding == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			body = zr
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet {
			encodings["models"] = encoding
			_, _ = w.Write([]byte(`{"models": [{"name": "jev-latest"}]}`))
			return
		}
		var wire map[string]any
		if err := json.NewDecoder(body).Decode(&wire); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		state, _ := wire["state"].(string)
		encodings[state[:1]] = encoding
		if state[:1] == "x" && attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(sampleBody(noulAnswer)))
	},
		WithCompression("gzip", func(b []byte) ([]byte, error) { compressCalls.Add(1); return gzipCompress(b) }),
		WithRetryPolicy(fastRetry(1)),
		WithHeaders(map[string]string{"Content-Encoding": "br"}),
	)
	if _, err := client.Ask(t.Context(), noulRequest(strings.Repeat("x", minCompressedBodyBytes))); err != nil {
		t.Fatalf("Ask large: %v", err)
	}
	if _, err := client.Ask(t.Context(), noulRequest("y")); err != nil {
		t.Fatalf("Ask small: %v", err)
	}
	if _, err := client.ListModels(t.Context()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := map[string]string{"x": "gzip", "y": "", "models": ""}
	for k, v := range want {
		if encodings[k] != v {
			t.Errorf("Content-Encoding for %s = %q, want %q", k, encodings[k], v)
		}
	}
	// One compression for the large body, reused by its retry.
	if got := compressCalls.Load(); got != 1 {
		t.Errorf("compress calls = %d, want 1", got)
	}
}

func TestCompressionError(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	client := newServer(t, func(http.ResponseWriter, *http.Request) { t.Error("request reached the server") },
		WithCompression("zstd", func([]byte) ([]byte, error) { return nil, errBoom }))
	if _, err := client.Ask(t.Context(), noulRequest(strings.Repeat("x", minCompressedBodyBytes))); !errors.Is(err, errBoom) {
		t.Errorf("Ask error = %v, want errBoom", err)
	}
}

func TestWithCompressionRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		encoding string
		compress func([]byte) ([]byte, error)
	}{
		{"empty encoding", "", gzipCompress},
		{"list of encodings", "gzip, zstd", gzipCompress},
		{"nil function", "zstd", nil},
	} {
		if _, err := NewClient(WithAPIKey("k"), WithCompression(tt.encoding, tt.compress)); err == nil {
			t.Errorf("%s: NewClient succeeded, want an error", tt.name)
		}
	}
}

func TestCompressedMultipartBody(t *testing.T) {
	t.Parallel()
	var got captured
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "gzip" {
			http.Error(w, "not compressed", http.StatusBadRequest)
			return
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(zr)
		if got, err = parseRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(sampleBody(noulAnswer)))
	}, WithCompression("gzip", gzipCompress))
	large := bytes.Repeat(pngBytes, 200)
	if _, err := client.Ask(t.Context(), noulRequest(NewImage(large))); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !bytes.Equal(got.parts["asset-0"].data, large) {
		t.Errorf("decompressed image part has %d bytes, want %d", len(got.parts["asset-0"].data), len(large))
	}
}
