// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"maps"
	"mime/multipart"
	"net/textproto"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"weak"
)

// Content types of the image formats recognized by their file signatures, to
// label upload parts. The API detects formats itself, so unrecognized images
// are still sent, labeled as generic binary data.
var imageSignatures = []struct {
	signature   []byte
	contentType string
}{
	{[]byte("\x89PNG\r\n\x1a\n"), "image/png"},
	{[]byte("\xff\xd8\xff"), "image/jpeg"},
}

const unknownContentType = "application/octet-stream"

// Image is an image asset backed by image file bytes or a path to an image
// file. Construct one with [NewImage], [ImageFromFile], [ImageFromBase64], or
// [EncodeImage], and always use it as a *Image.
//
// An Image is used in a request in one of three ways:
//
//   - placed directly in content, such as Request.State or a question's
//     Instructions, where it is sent in place;
//   - listed in Request.Assets under an alias, which text in the request then
//     uses to refer to it;
//   - referenced inside text through its String form, such as
//     fmt.Sprintf("Product: %s", photo), while attached with
//     [Client.AttachAssets].
//
// A reference to an image that is neither placed, listed, nor attached is
// sent as plain text, without the image. Image assets require a
// vision-language model.
//
// An Image is read once, on first use, and reused by every request that sends
// it. It is safe for concurrent use.
type Image struct {
	ref  string
	data []byte
	path string

	mu       sync.Mutex
	resolved *resolvedImage
}

// resolvedImage is an image with its data read and validated.
type resolvedImage struct {
	contentType string
	data        []byte
}

// NewImage returns an image backed by data, the bytes of an image file. The
// Image keeps data without copying it, so data must not be modified
// afterwards.
func NewImage(data []byte) *Image { return newImage(data, "") }

// ImageFromFile returns an image read from the file at path when it is first
// sent.
func ImageFromFile(path string) *Image { return newImage(nil, path) }

// ImageFromBase64 returns an image from the base64-encoded (standard, padded)
// bytes of an image file. Whitespace, such as the line breaks of wrapped
// base64, is ignored.
func ImageFromBase64(text string) (*Image, error) {
	text = strings.Join(strings.Fields(text), "")
	if text == "" {
		return nil, errors.New("image data must contain non-empty base64 text")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("image data must contain valid base64 text: %w", err)
	}
	return NewImage(data), nil
}

// EncodeImage returns an image encoding img in format, "jpeg" (or "jpg") or
// "png". JPEG uses quality 95 and cannot store transparency, so use "png" for
// images with an alpha channel. Encode the image yourself if different
// settings are needed.
func EncodeImage(img image.Image, format string) (*Image, error) {
	var buf bytes.Buffer
	var err error
	switch strings.ToLower(format) {
	case "jpeg", "jpg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	case "png":
		err = png.Encode(&buf, img)
	default:
		return nil, fmt.Errorf("unsupported image format %q: want jpeg or png", format)
	}
	if err != nil {
		return nil, fmt.Errorf("encoding image as %s: %w", format, err)
	}
	return NewImage(buf.Bytes()), nil
}

// liveImages maps each live image's reference to the image, so an image
// placed directly in content is found from its reference in the encoded body.
// Entries are removed once their image is garbage collected.
var liveImages sync.Map // reference -> weak.Pointer[Image]

func newImage(data []byte, path string) *Image {
	var id [16]byte
	_, _ = rand.Read(id[:]) // crypto/rand.Read never returns an error.
	img := &Image{ref: "<image:" + hex.EncodeToString(id[:]) + ">", data: data, path: path}
	liveImages.Store(img.ref, weak.Make(img))
	runtime.AddCleanup(img, func(ref string) { liveImages.Delete(ref) }, img.ref)
	return img
}

func liveImage(ref string) *Image {
	p, ok := liveImages.Load(ref)
	if !ok {
		return nil
	}
	return p.(weak.Pointer[Image]).Value()
}

// String returns the image's unique reference, for using it inside text.
func (img *Image) String() string {
	if img == nil {
		return "<nil>"
	}
	return img.ref
}

// MarshalJSON encodes the image as its reference; the request carries its
// bytes separately.
func (img *Image) MarshalJSON() ([]byte, error) {
	// The reference is JSON-safe ASCII.
	return []byte(`"` + img.ref + `"`), nil
}

// resolve reads and validates the image once. A failure is not cached, so a
// later request retries it.
func (img *Image) resolve() (*resolvedImage, error) {
	img.mu.Lock()
	defer img.mu.Unlock()
	if img.resolved != nil {
		return img.resolved, nil
	}
	data := img.data
	if img.path != "" {
		var err error
		if data, err = os.ReadFile(img.path); err != nil {
			return nil, fmt.Errorf("image file could not be read: %w", err)
		}
	}
	if len(data) == 0 {
		return nil, errors.New("image data is empty")
	}
	img.resolved = &resolvedImage{contentType: imageContentType(data), data: data}
	return img.resolved, nil
}

func imageContentType(data []byte) string {
	for _, s := range imageSignatures {
		if bytes.HasPrefix(data, s.signature) {
			return s.contentType
		}
	}
	return unknownContentType
}

// attachedAssets are a client's attached images, keyed by reference and
// shared by its calls from any goroutine. Overlapping attachments may hold
// the same image; it stays attached until the last of them is detached.
type attachedAssets struct {
	mu     sync.Mutex
	images map[string]*Image
	holds  map[string]int
}

func (a *attachedAssets) attach(images []*Image) (detach func()) {
	images = slices.DeleteFunc(slices.Clone(images), func(img *Image) bool { return img == nil })
	a.mu.Lock()
	if a.images == nil {
		a.images, a.holds = make(map[string]*Image), make(map[string]int)
	}
	for _, img := range images {
		a.images[img.ref] = img
		a.holds[img.ref]++
	}
	a.mu.Unlock()
	return sync.OnceFunc(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, img := range images {
			if a.holds[img.ref]--; a.holds[img.ref] == 0 {
				delete(a.holds, img.ref)
				delete(a.images, img.ref)
			}
		}
	})
}

func (a *attachedAssets) snapshot() map[string]*Image {
	a.mu.Lock()
	defer a.mu.Unlock()
	return maps.Clone(a.images)
}

// referencePattern matches an image reference in an encoded body, with or
// without encoding/json's HTML escaping of the angle brackets.
var referencePattern = regexp.MustCompile(`(?:<|\\u003c)image:([0-9a-f]{32})(?:>|\\u003e)`)

// namedImage is one entry of a request's assets.
type namedImage struct {
	name  string
	image *Image
}

// placeAssets finds the images a System One body sends. It returns the body,
// with images listed in aliases written as their alias where they are placed,
// and the images to send: those in aliases, sorted by alias, then those whose
// reference appears in the body, in order of appearance. A reference counts
// when its image is listed or attached, or when it is a whole JSON string,
// which is how a placed image encodes.
func placeAssets(body []byte, aliases, attached map[string]*Image) ([]byte, []namedImage) {
	// An image listed under several aliases is written as the first of them.
	names := slices.Sorted(maps.Keys(aliases))
	aliasOf := make(map[*Image]string, len(aliases))
	known := make(map[string]*Image, len(aliases)+len(attached))
	maps.Copy(known, attached)
	pending := make([]namedImage, 0, len(aliases))
	seen := make(map[string]bool, len(aliases))
	for _, name := range names {
		img := aliases[name]
		if _, ok := aliasOf[img]; !ok {
			aliasOf[img] = name
		}
		known[img.ref] = img
		pending = append(pending, namedImage{name, img})
		seen[name] = true
	}

	var out []byte
	last := 0
	for _, m := range referencePattern.FindAllSubmatchIndex(body, -1) {
		start, end := m[0], m[1]
		ref := "<image:" + string(body[m[2]:m[3]]) + ">"
		img, whole := known[ref], isWholeString(body, start, end)
		if img == nil && whole {
			img = liveImage(ref)
		}
		if img == nil {
			continue
		}
		if alias, ok := aliasOf[img]; ok && whole {
			quoted, _ := json.Marshal(alias) // Marshaling a string cannot fail.
			out = append(append(out, body[last:start]...), quoted[1:len(quoted)-1]...)
			last = end
			continue
		}
		if !seen[ref] {
			seen[ref] = true
			pending = append(pending, namedImage{ref, img})
		}
	}
	if out != nil {
		body = append(out, body[last:]...)
	}
	return body, pending
}

// isWholeString reports whether body[start:end] is an entire JSON string
// value or key: it is directly preceded by an unescaped quote and followed by
// a quote. A match never ends in a backslash, so the closing quote cannot be
// escaped, and in valid JSON an unescaped quote before a match opens the
// string.
func isWholeString(body []byte, start, end int) bool {
	if start == 0 || end >= len(body) || body[start-1] != '"' || body[end] != '"' {
		return false
	}
	backslashes := 0
	for i := start - 2; i >= 0 && body[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 0
}

// assetPartName names the multipart part of the asset at index i, so the body
// can name a part before its image is read.
func assetPartName(i int) string { return fmt.Sprintf("asset-%d", i) }

// withAssets adds an "assets" object naming each image's part to the encoded
// JSON object body.
func withAssets(body []byte, images []namedImage) []byte {
	type entry struct {
		Type string `json:"type"`
		Part string `json:"part"`
	}
	entries := make(map[string]entry, len(images))
	for i, n := range images {
		entries[n.name] = entry{Type: "image", Part: assetPartName(i)}
	}
	encoded, _ := json.Marshal(entries) // Strings only; cannot fail.
	body = bytes.TrimRight(body, " \n")
	return slices.Concat(body[:len(body)-1], []byte(`,"assets":`), encoded, []byte("}"))
}

// encodeMultipart encodes the JSON body as the first part, named "request",
// of a multipart/form-data body, followed by each image as the part its asset
// entry names. The API requires each part to be claimed by exactly one asset,
// so an image under several names is sent once per name.
func encodeMultipart(body []byte, images []*resolvedImage) (contentType string, encoded []byte, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	write := func(header textproto.MIMEHeader, content []byte) error {
		part, err := w.CreatePart(header)
		if err != nil {
			return err
		}
		_, err = part.Write(content)
		return err
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="request"`)
	header.Set("Content-Type", "application/json")
	if err := write(header, body); err != nil {
		return "", nil, err
	}
	for i, img := range images {
		name := assetPartName(i)
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, name))
		header.Set("Content-Type", img.contentType)
		if err := write(header, img.data); err != nil {
			return "", nil, err
		}
	}
	if err := w.Close(); err != nil {
		return "", nil, err
	}
	return w.FormDataContentType(), buf.Bytes(), nil
}
