// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package server_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/gsoultan/mimir/audio"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// tone returns a WAV file of seconds of a wavering tone at rate.
func tone(t *testing.T, seconds float64, rate int) []byte {
	t.Helper()
	n := int(seconds * float64(rate))
	s := make([]float32, n)
	for i := range s {
		x := float64(i) / float64(rate)
		s[i] = float32(0.3 * math.Sin(2*math.Pi*440*x) * math.Sin(2*math.Pi*3*x))
	}
	var b bytes.Buffer
	if err := audio.WriteWAV(&b, audio.Clip{Samples: s, Rate: rate}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func photo(t *testing.T, c color.Color, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestSpeechToText uploads a ZIP of a tiny Hugging Face Whisper folder and
// has it write down a recording.
func TestSpeechToText(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	res, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Whisper", Architecture: "mim", Weights: fixture(t, "tiny-whisper.zip")}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg.GetModel()
	if m.GetArchitecture() != "speech-to-text" {
		t.Fatalf("architecture %q", m.GetArchitecture())
	}
	// At 44.1 kHz, resampled to the model's 16.
	tr, err := c.media.Transcribe(ctx, connect.NewRequest(&mimirv1.TranscribeRequest{ModelId: m.GetId(), Recording: tone(t, 2.5, 44100)}))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Msg.GetLanguage() == "" || math.Abs(tr.Msg.GetSeconds()-2.5) > 0.01 {
		t.Errorf("language %q, %v s", tr.Msg.GetLanguage(), tr.Msg.GetSeconds())
	}
	for _, p := range tr.Msg.GetParts() {
		if p.GetEnd() > tr.Msg.GetSeconds() {
			t.Errorf("a part ends at %v", p.GetEnd())
		}
	}
	for _, tc := range []struct {
		req  *mimirv1.TranscribeRequest
		want string
	}{
		{&mimirv1.TranscribeRequest{ModelId: m.GetId()}, "Send a recording"},
		{&mimirv1.TranscribeRequest{ModelId: m.GetId(), Recording: []byte("not a wav")}, "isn't a WAV file"},
		{&mimirv1.TranscribeRequest{ModelId: m.GetId(), Recording: tone(t, 0.1, 16000), Language: "xx"}, "doesn't know the language"},
	} {
		if _, err := c.media.Transcribe(ctx, connect.NewRequest(tc.req)); code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v, want %q", err, tc.want)
		}
	}
	// It doesn't answer Predict, run on workers or in a browser, and
	// MatchPhotos needs a photo search model.
	if _, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId()})); code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "Transcribe") {
		t.Errorf("Predict: %v", err)
	}
	if _, err := c.media.MatchPhotos(ctx, connect.NewRequest(&mimirv1.MatchPhotosRequest{ModelId: m.GetId(), Photos: [][]byte{photo(t, color.White, 8, 8)}, Texts: []string{"a"}})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("MatchPhotos with a speech model: %v", err)
	}
}

// TestPhotoSearch uploads a ZIP of a tiny Hugging Face CLIP folder and has
// it match photos and words.
func TestPhotoSearch(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	res, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "CLIP", Architecture: "photo-search", Weights: fixture(t, "tiny-clip.zip")}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg.GetModel()
	if m.GetArchitecture() != "photo-search" {
		t.Fatalf("architecture %q", m.GetArchitecture())
	}
	photos := [][]byte{photo(t, color.RGBA{200, 30, 30, 255}, 64, 40), photo(t, color.RGBA{30, 30, 200, 255}, 1000, 900)}
	r, err := c.media.MatchPhotos(ctx, connect.NewRequest(&mimirv1.MatchPhotosRequest{ModelId: m.GetId(), Photos: photos, Texts: []string{"cat", "dog", "a cat"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Msg.GetPhotos()) != 2 {
		t.Fatalf("%d photos", len(r.Msg.GetPhotos()))
	}
	for _, p := range r.Msg.GetPhotos() {
		var sum float32
		for _, x := range p.GetChance() {
			sum += x
		}
		if len(p.GetSimilarity()) != 3 || math.Abs(float64(sum)-1) > 1e-4 {
			t.Errorf("match %v", p)
		}
		for _, s := range p.GetSimilarity() {
			if s < -1.001 || s > 1.001 {
				t.Errorf("similarity %v", s)
			}
		}
	}
	for _, tc := range []struct {
		req  *mimirv1.MatchPhotosRequest
		want string
	}{
		{&mimirv1.MatchPhotosRequest{ModelId: m.GetId(), Photos: photos}, "at least one photo and one text"},
		{&mimirv1.MatchPhotosRequest{ModelId: m.GetId(), Photos: [][]byte{[]byte("nope")}, Texts: []string{"a"}}, "Photo 1 can't be read"},
		{&mimirv1.MatchPhotosRequest{ModelId: m.GetId(), Photos: photos, Texts: []string{" "}}, "can't be empty"},
	} {
		if _, err := c.media.MatchPhotos(ctx, connect.NewRequest(tc.req)); code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v, want %q", err, tc.want)
		}
	}
	if _, err := c.media.Transcribe(ctx, connect.NewRequest(&mimirv1.TranscribeRequest{ModelId: m.GetId(), Recording: tone(t, 0.5, 16000)})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("Transcribe with a photo model: %v", err)
	}
}
