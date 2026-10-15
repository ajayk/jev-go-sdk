// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
)

var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\nrest-of-png")
	jpegBytes = []byte("\xff\xd8\xffrest-of-jpeg")
)

// part is one multipart part as the server received it.
type part struct {
	contentType string
	filename    string
	data        []byte
}

// captured is one request as the server received it.
type captured struct {
	header http.Header
	body   map[string]any
	parts  map[string]part
}

// captureServer returns a client whose server records each request and
// answers every question id in answers.
func captureServer(t *testing.T, answers string, opts ...Option) (*Client, func() []captured) {
	t.Helper()
	var mu sync.Mutex
	var got []captured
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		c, err := parseRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
		_, _ = w.Write([]byte(sampleBody(answers)))
	}, opts...)
	return client, func() []captured {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func parseRequest(r *http.Request) (captured, error) {
	c := captured{header: r.Header.Clone()}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return c, err
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return c, err
	}
	if mediaType != "multipart/form-data" {
		return c, json.Unmarshal(raw, &c.body)
	}
	c.parts = make(map[string]part)
	mr := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return c, err
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return c, err
		}
		if p.FormName() == "request" {
			if p.Header.Get("Content-Type") != "application/json" {
				return c, fmt.Errorf("request part content type %q", p.Header.Get("Content-Type"))
			}
			if err := json.Unmarshal(data, &c.body); err != nil {
				return c, err
			}
			continue
		}
		c.parts[p.FormName()] = part{contentType: p.Header.Get("Content-Type"), filename: p.FileName(), data: data}
	}
	return c, nil
}

const noulAnswer = `{"q": {"type": "noul", "noul": 0.5}}`

func noulRequest(state Content) Request {
	return Request{State: state, Questions: map[string]Question{"q": Noul{Instructions: "Is it damaged?"}}}
}

func TestImageReferenceFormat(t *testing.T) {
	t.Parallel()
	a, b := NewImage(pngBytes), NewImage(pngBytes)
	if !regexp.MustCompile(`^<image:[0-9a-f]{32}>$`).MatchString(a.String()) {
		t.Errorf("String() = %q, want <image:32 hex digits>", a.String())
	}
	if a.String() == b.String() {
		t.Errorf("two images share the reference %q", a.String())
	}
	if got, want := (*Image)(nil).String(), "<nil>"; got != want {
		t.Errorf("nil String() = %q, want %q", got, want)
	}
}

func TestPlacedImageIsSentAsPart(t *testing.T) {
	t.Parallel()
	client, requests := captureServer(t, noulAnswer)
	photo := NewImage(pngBytes)
	if _, err := client.Ask(t.Context(), noulRequest(photo)); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := requests()[0]
	if ct := got.header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
		t.Errorf("Content-Type = %q, want multipart/form-data", ct)
	}
	if got.body["state"] != photo.String() {
		t.Errorf("state = %v, want the image reference %q", got.body["state"], photo)
	}
	wantAssets := map[string]any{photo.String(): map[string]any{"type": "image", "part": "asset-0"}}
	if !reflect.DeepEqual(got.body["assets"], wantAssets) {
		t.Errorf("assets = %v, want %v", got.body["assets"], wantAssets)
	}
	wantParts := map[string]part{"asset-0": {contentType: "image/png", filename: "asset-0", data: pngBytes}}
	if !reflect.DeepEqual(got.parts, wantParts) {
		t.Errorf("parts = %+v, want %+v", got.parts, wantParts)
	}
}

func TestImagesInQuestionsAndNestedContent(t *testing.T) {
	t.Parallel()
	client, requests := captureServer(t, `{"q": {"type": "noul", "noul": 0.5}, "b": {"type": "box", "box": {"top": 0.1, "bottom": 0.6, "left": 0.25, "right": 0.75}, "confidence": 0.9}}`)
	inState, inQuestion, inCriteria := NewImage(pngBytes), NewImage(jpegBytes), NewImage([]byte("GIF89a"))
	req := Request{
		State: map[string]any{"photos": []any{inState}},
		Questions: map[string]Question{
			"q": Noul{Instructions: inQuestion, True: map[string]any{"example": inCriteria}},
			"b": Box{Instructions: "The dog"},
		},
	}
	if _, err := client.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := requests()[0]
	assets, _ := got.body["assets"].(map[string]any)
	if len(assets) != 3 || len(got.parts) != 3 {
		t.Fatalf("got %d assets and %d parts, want 3 of each: %v", len(assets), len(got.parts), assets)
	}
	wantTypes := map[*Image]string{inState: "image/png", inQuestion: "image/jpeg", inCriteria: unknownContentType}
	for img, contentType := range wantTypes {
		entry, ok := assets[img.String()].(map[string]any)
		if !ok {
			t.Errorf("no asset entry for %s", img)
			continue
		}
		p := got.parts[entry["part"].(string)]
		if p.contentType != contentType || !bytes.Equal(p.data, img.data) {
			t.Errorf("part for %s: got %q %q, want %q %q", img, p.contentType, p.data, contentType, img.data)
		}
	}
}

func TestAssetAliases(t *testing.T) {
	t.Parallel()
	client, requests := captureServer(t, noulAnswer)
	photo := NewImage(pngBytes)

	// Referenced by alias in text, and placed directly: sent once, as its alias.
	req := noulRequest(map[string]any{"text": "Look at <photo>", "image": photo})
	req.Assets = map[string]*Image{"<photo>": photo}
	if _, err := client.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := requests()[0]
	wantState := map[string]any{"text": "Look at <photo>", "image": "<photo>"}
	if !reflect.DeepEqual(got.body["state"], wantState) {
		t.Errorf("state = %v, want %v", got.body["state"], wantState)
	}
	wantAssets := map[string]any{"<photo>": map[string]any{"type": "image", "part": "asset-0"}}
	if !reflect.DeepEqual(got.body["assets"], wantAssets) {
		t.Errorf("assets = %v, want %v", got.body["assets"], wantAssets)
	}
}

func TestImageUnderSeveralAliasesIsSentPerAlias(t *testing.T) {
	t.Parallel()
	client, requests := captureServer(t, noulAnswer)
	photo := NewImage(pngBytes)
	req := noulRequest("Compare <a> with <b>")
	req.Assets = map[string]*Image{"<b>": photo, "<a>": photo}
	if _, err := client.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := requests()[0]
	wantAssets := map[string]any{
		"<a>": map[string]any{"type": "image", "part": "asset-0"},
		"<b>": map[string]any{"type": "image", "part": "asset-1"},
	}
	if !reflect.DeepEqual(got.body["assets"], wantAssets) {
		t.Errorf("assets = %v, want %v", got.body["assets"], wantAssets)
	}
	if len(got.parts) != 2 {
		t.Errorf("got %d parts, want 2", len(got.parts))
	}
}

func TestAttachedImagesInText(t *testing.T) {
	t.Parallel()
	client, requests := captureServer(t, noulAnswer)
	photo := NewImage(pngBytes)
	state := fmt.Sprintf("Product: %s", photo)

	// Not attached: the reference is plain text.
	if _, err := client.Ask(t.Context(), noulRequest(state)); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	detachOuter := client.AttachAssets(photo, nil)
	detachInner := client.AttachAssets(photo)
	if _, err := client.Ask(t.Context(), noulRequest(state)); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// Overlapping attachments: still attached until the last detaches.
	detachInner()
	detachInner()
	if _, err := client.Ask(t.Context(), noulRequest(state)); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	detachOuter()
	if _, err := client.Ask(t.Context(), noulRequest(state)); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	for i, wantSent := range []bool{false, true, true, false} {
		got := requests()[i]
		assets, _ := got.body["assets"].(map[string]any)
		_, sent := assets[photo.String()]
		if sent != wantSent {
			t.Errorf("request %d: image sent = %t, want %t (body %v)", i, sent, wantSent, got.body)
		}
		if got.body["state"] != state {
			t.Errorf("request %d: state = %v, want %q", i, got.body["state"], state)
		}
	}
}

func TestReferenceOfUnplacedImageIsPlainText(t *testing.T) {
	t.Parallel()
	client, requests := captureServer(t, noulAnswer)
	photo := NewImage(pngBytes)
	// Inside text, or quoted inside text, a reference alone does not send its
	// image.
	for _, state := range []string{"Product: " + photo.String(), `say "` + photo.String() + `"`} {
		if _, err := client.Ask(t.Context(), noulRequest(state)); err != nil {
			t.Fatalf("Ask: %v", err)
		}
	}
	for i, got := range requests() {
		if got.parts != nil || got.body["assets"] != nil {
			t.Errorf("request %d sent assets: %v", i, got.body)
		}
	}
}

func TestExtraAssetsReplaceImages(t *testing.T) {
	t.Parallel()
	client, requests := captureServer(t, noulAnswer)
	req := noulRequest(NewImage(pngBytes))
	req.Extra = map[string]any{"assets": map[string]any{"x": map[string]any{"type": "image", "data_base64": "AA==", "format": "png"}}}
	if _, err := client.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := requests()[0]
	if got.parts != nil {
		t.Errorf("sent multipart with Extra[\"assets\"]: %v", got.parts)
	}
	if !reflect.DeepEqual(got.body["assets"], req.Extra["assets"]) {
		t.Errorf("assets = %v, want %v", got.body["assets"], req.Extra["assets"])
	}
}

func TestImageFileIsReadOnce(t *testing.T) {
	t.Parallel()
	client, requests := captureServer(t, noulAnswer)
	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	photo := ImageFromFile(path)
	if _, err := client.Ask(t.Context(), noulRequest(photo)); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Ask(t.Context(), noulRequest(photo)); err != nil {
		t.Fatalf("Ask after the file was removed: %v", err)
	}
	for i, got := range requests() {
		if p := got.parts["asset-0"]; !bytes.Equal(p.data, pngBytes) {
			t.Errorf("request %d: part data = %q, want %q", i, p.data, pngBytes)
		}
	}
}

func TestInvalidImageNamesItsAsset(t *testing.T) {
	t.Parallel()
	client, _ := captureServer(t, noulAnswer)
	req := noulRequest("Look at <photo>")
	req.Assets = map[string]*Image{"<photo>": ImageFromFile(filepath.Join(t.TempDir(), "missing.png"))}
	_, err := client.Ask(t.Context(), req)
	if !errors.Is(err, ErrInvalidRequest) || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), `asset "<photo>"`) {
		t.Errorf("Ask error = %v, want ErrInvalidRequest naming asset \"<photo>\" and wrapping os.ErrNotExist", err)
	}
}

func TestImageFromBase64(t *testing.T) {
	t.Parallel()
	img, err := ImageFromBase64("iVBORw0K\nGgpyZXN0LW9mLXBuZw==\n")
	if err != nil {
		t.Fatalf("ImageFromBase64: %v", err)
	}
	if !bytes.Equal(img.data, pngBytes) {
		t.Errorf("data = %q, want %q", img.data, pngBytes)
	}
	for _, text := range []string{"", " \n", "not base64!", "iVBORw0KGgo", "aé=="} {
		if _, err := ImageFromBase64(text); err == nil {
			t.Errorf("ImageFromBase64(%q) succeeded, want an error", text)
		}
	}
}

func TestEncodeImage(t *testing.T) {
	t.Parallel()
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	src.Set(1, 1, color.RGBA{R: 255, A: 255})
	for format, contentType := range map[string]string{"png": "image/png", "jpeg": "image/jpeg", "JPG": "image/jpeg"} {
		img, err := EncodeImage(src, format)
		if err != nil {
			t.Fatalf("EncodeImage(%q): %v", format, err)
		}
		resolved, err := img.resolve()
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if resolved.contentType != contentType {
			t.Errorf("EncodeImage(%q) content type = %q, want %q", format, resolved.contentType, contentType)
		}
	}
	if _, err := EncodeImage(src, "webp"); err == nil {
		t.Error("EncodeImage(webp) succeeded, want an error")
	}
}

func TestIsWholeString(t *testing.T) {
	t.Parallel()
	ref := "<image:0123456789abcdef0123456789abcdef>"
	tests := []struct {
		body string
		want bool
	}{
		{`{"state":"` + ref + `"}`, true},
		{`{"` + ref + `":1}`, true},
		{`{"state":"x` + ref + `"}`, false},
		{`{"state":"` + ref + `x"}`, false},
		{`{"state":"say \"` + ref + `\""}`, false},
		{`{"state":"a\\\\","b":"` + ref + `"}`, true},
	}
	for _, tt := range tests {
		start := strings.Index(tt.body, ref)
		if got := isWholeString([]byte(tt.body), start, start+len(ref)); got != tt.want {
			t.Errorf("isWholeString(%s) = %t, want %t", tt.body, got, tt.want)
		}
	}
}
