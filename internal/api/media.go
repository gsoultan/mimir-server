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

package api

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/memguard"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Bounds on a photo search request.
const (
	MaxMatchPhotos = 64
	MaxMatchTexts  = 64
	maxMatchText   = 300 // characters; CLIP reads about 75 words
	// photoSide is the shorter side photos are shrunk to before the model
	// prepares them: well above the 224 pixels CLIP reads, and small
	// enough that 64 phone photos fit in a few tens of megabytes.
	photoSide = 448
)

// mediaModel returns model m for a media request when it answers with
// want, and why not otherwise.
func (s *Service) mediaModel(ctx context.Context, id string, want arch.Output) (store.Model, error) {
	m, err := s.get(ctx, id)
	if err != nil {
		return store.Model{}, err
	}
	a, ok := arch.Lookup(m.Architecture)
	if !ok || a.Answers() != want {
		what := map[arch.Output]string{arch.Transcript: "a speech to text model", arch.Matches: "a photo search model"}[want]
		return store.Model{}, invalid("%s isn't %s.", m.Name, what)
	}
	if !s.Local {
		return store.Model{}, connect.NewError(connect.CodeUnavailable, errors.New(a.Title+" models run on the console itself, and this server leaves running models to workers. Start it as standalone to use them."))
	}
	if p, ok := auth.From(ctx); ok {
		if err := s.admit(ctx, p); err != nil {
			return store.Model{}, err
		}
	}
	return m, nil
}

// mediaError turns a failed run into what to tell the caller.
func (s *Service) mediaError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, runtime.ErrBadInput):
		return invalid("%s.", upper(strings.TrimPrefix(err.Error(), runtime.ErrBadInput.Error()+": ")))
	case tooBig(err):
		return tooBigError(err)
	case errors.Is(err, memguard.ErrLowMemory):
		return lowMemory()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeCanceled, errors.New("The request ended before the model finished."))
	}
	return s.internal(ctx, err)
}

func upper(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[n:]
}

func (s *Service) weightsOf(m store.Model) runtime.Loader {
	return func() (runtime.Weights, error) { return s.store.OpenWeights(m.ID) }
}

// Transcribe writes down what is said in a recording.
func (s *Service) Transcribe(ctx context.Context, req *connect.Request[mimirv1.TranscribeRequest]) (*connect.Response[mimirv1.TranscribeResponse], error) {
	msg := req.Msg
	lang := strings.ToLower(strings.TrimSpace(msg.GetLanguage()))
	switch {
	case len(msg.GetRecording()) == 0:
		return nil, invalid("Send a recording to write down.")
	case len(lang) > 16:
		return nil, invalid("A language is a short code such as en or de.")
	}
	m, err := s.mediaModel(ctx, msg.GetModelId(), arch.Transcript)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	h, err := s.cache.Transcribe(ctx, m.ID, m.WeightsVersion(), s.weightsOf(m), msg.GetRecording(), runtime.Hearing{Language: lang, Translate: msg.GetTranslate()})
	if err != nil {
		return nil, s.mediaError(ctx, err)
	}
	out := &mimirv1.TranscribeResponse{Text: h.Text(), Language: h.Language, Seconds: h.Seconds, DurationMicros: time.Since(start).Microseconds()}
	for _, p := range h.Segments {
		out.Parts = append(out.Parts, &mimirv1.TranscriptPart{Start: p.Start, End: min(p.End, h.Seconds), Text: p.Text})
	}
	return connect.NewResponse(out), nil
}

// MatchPhotos says how well each photo fits each text.
func (s *Service) MatchPhotos(ctx context.Context, req *connect.Request[mimirv1.MatchPhotosRequest]) (*connect.Response[mimirv1.MatchPhotosResponse], error) {
	msg := req.Msg
	switch n, t := len(msg.GetPhotos()), len(msg.GetTexts()); {
	case n == 0 || t == 0:
		return nil, invalid("Send at least one photo and one text.")
	case n > MaxMatchPhotos:
		return nil, invalid("Send at most %d photos at once.", MaxMatchPhotos)
	case t > MaxMatchTexts:
		return nil, invalid("Send at most %d texts at once.", MaxMatchTexts)
	}
	texts := make([]string, len(msg.GetTexts()))
	for i, t := range msg.GetTexts() {
		t = strings.TrimSpace(t)
		switch {
		case t == "":
			return nil, invalid("A text to match can't be empty.")
		case !utf8.ValidString(t):
			return nil, invalid("Texts must be valid text.")
		case utf8.RuneCountInString(t) > maxMatchText:
			return nil, invalid("A text to match can be at most %d characters; the model reads a short phrase.", maxMatchText)
		}
		texts[i] = t
	}
	m, err := s.mediaModel(ctx, msg.GetModelId(), arch.Matches)
	if err != nil {
		return nil, err
	}
	photos := make([]image.Image, len(msg.GetPhotos()))
	for i, b := range msg.GetPhotos() {
		if photos[i], err = decodePhoto(b); err != nil {
			return nil, invalid("Photo %d can't be read: it must be a JPG, PNG or GIF of at most %d MB and 50 megapixels.", i+1, schema.MaxPhotoBytes>>20)
		}
	}
	start := time.Now()
	r, err := s.cache.MatchPhotos(ctx, m.ID, m.WeightsVersion(), s.weightsOf(m), photos, texts)
	if err != nil {
		return nil, s.mediaError(ctx, err)
	}
	out := &mimirv1.MatchPhotosResponse{DurationMicros: time.Since(start).Microseconds()}
	for i := range r.Similarity {
		pm := &mimirv1.PhotoMatch{}
		for j := range r.Similarity[i] {
			pm.Similarity = append(pm.Similarity, float32(r.Similarity[i][j]))
			pm.Chance = append(pm.Chance, float32(r.Chance[i][j]))
		}
		out.Photos = append(out.Photos, pm)
	}
	return connect.NewResponse(out), nil
}

// decodePhoto reads a JPG, PNG or GIF and shrinks it, by averaging, until
// its shorter side is photoSide.
func decodePhoto(b []byte) (image.Image, error) {
	if len(b) > schema.MaxPhotoBytes {
		return nil, schema.ErrBadPhoto
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > 50_000_000 {
		return nil, schema.ErrBadPhoto
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, schema.ErrBadPhoto
	}
	return shrinkTo(img, photoSide), nil
}

// shrinkTo returns img with its shorter side made side by averaging each
// block of pixels, or img itself when it is no bigger.
func shrinkTo(img image.Image, side int) image.Image {
	r := img.Bounds()
	short := min(r.Dx(), r.Dy())
	if short <= side {
		return img
	}
	w, h := r.Dx()*side/short, r.Dy()*side/short
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		y0, y1 := r.Min.Y+y*r.Dy()/h, r.Min.Y+(y+1)*r.Dy()/h
		for x := range w {
			x0, x1 := r.Min.X+x*r.Dx()/w, r.Min.X+(x+1)*r.Dx()/w
			var sr, sg, sb, n uint64
			for yy := y0; yy < max(y1, y0+1); yy++ {
				for xx := x0; xx < max(x1, x0+1); xx++ {
					c := color.NRGBAModel.Convert(img.At(xx, yy)).(color.NRGBA)
					sr, sg, sb, n = sr+uint64(c.R), sg+uint64(c.G), sb+uint64(c.B), n+1
				}
			}
			out.SetNRGBA(x, y, color.NRGBA{uint8((sr + n/2) / n), uint8((sg + n/2) / n), uint8((sb + n/2) / n), 255})
		}
	}
	return out
}
