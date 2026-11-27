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

package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"time"

	mimfile "github.com/gsoultan/mimir-model"
	"github.com/gsoultan/mimir/audio"
	"github.com/gsoultan/mimir/audio/whisper"
	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/vision/clip"
)

// MediaKind returns what a Mimir Model file holds by its metadata:
// "whisper" for speech to text, "clip" for photo search, or "" for
// anything else.
func MediaKind(b []byte) string {
	m, err := mim.Parse(b)
	if err != nil {
		return ""
	}
	defer m.Close()
	md, err := m.Metadata()
	if err != nil {
		return ""
	}
	switch a, _ := md["general.architecture"].(string); a {
	case "whisper", "clip":
		return a
	}
	return ""
}

// open parses a Mimir Model file and reads its metadata.
func open(b []byte) (*mim.Model, mimfile.Metadata, error) {
	m, err := mim.Parse(b)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	md, err := m.Metadata()
	if err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	return m, md, nil
}

// one is a turn to run a model that does one thing at a time, since each
// run already uses every core.
type one chan struct{}

func (o one) take(ctx context.Context) error {
	select {
	case o <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o one) give() { <-o }

// Speech is a loaded speech-to-text model (Whisper).
type Speech struct {
	m    *mim.Model
	w    *whisper.Model
	turn one
}

// OpenSpeech loads a Mimir Model file of a Whisper model. It fails with
// ErrBadWeights for a file that is not one or that mimir cannot run.
func OpenSpeech(b []byte) (*Speech, error) {
	m, md, err := open(b)
	if err != nil {
		return nil, err
	}
	w, err := whisper.Load(m, md, tensor.DefaultDevice())
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	return &Speech{m: m, w: w, turn: make(one, 1)}, nil
}

// Languages returns the codes of the languages the model knows.
func (s *Speech) Languages() []string { return s.w.Languages() }

// Close releases the model's file.
func (s *Speech) Close() error { return s.m.Close() }

// MaxRecording is the longest recording Transcribe takes.
const MaxRecording = 2 * time.Hour

// Hearing configures a transcription.
type Hearing struct {
	Language  string // a language code, or "" to detect it
	Translate bool   // into English
	Progress  func(done, total int)
}

// Heard is a transcription and the length of the recording.
type Heard struct {
	*whisper.Transcript
	Seconds float64
}

// Transcribe writes down the speech in wav, a WAV file. A recording that
// isn't a WAV file, or is longer than MaxRecording, fails with
// ErrBadInput, as does a language the model doesn't know.
func (s *Speech) Transcribe(ctx context.Context, wav []byte, h Hearing) (Heard, error) {
	clip, err := audio.ReadWAV(bytes.NewReader(wav))
	if err != nil {
		return Heard{}, fmt.Errorf("%w: the recording isn't a WAV file of plain samples", ErrBadInput)
	}
	if clip.Seconds() > MaxRecording.Seconds() {
		return Heard{}, fmt.Errorf("%w: the recording is %.0f minutes; send at most %.0f at once", ErrBadInput, clip.Seconds()/60, MaxRecording.Minutes())
	}
	if len(clip.Samples) == 0 {
		return Heard{}, fmt.Errorf("%w: the recording is empty", ErrBadInput)
	}
	rate := s.w.Config.SampleRate
	if clip.Rate != rate {
		clip = clip.Resample(rate)
	}
	if err := s.turn.take(ctx); err != nil {
		return Heard{}, err
	}
	defer s.turn.give()
	t, err := s.w.Transcribe(ctx, clip.Samples, whisper.Options{Language: h.Language, Translate: h.Translate, Progress: h.Progress})
	switch {
	case errors.Is(err, whisper.ErrLanguage):
		return Heard{}, fmt.Errorf("%w: this model doesn't know the language %q", ErrBadInput, h.Language)
	case errors.Is(err, errors.ErrUnsupported):
		return Heard{}, fmt.Errorf("%w: this model only knows English, so it can't translate", ErrBadInput)
	case err != nil:
		return Heard{}, err
	}
	return Heard{Transcript: t, Seconds: clip.Seconds()}, nil
}

// Photos is a loaded photo search model (CLIP).
type Photos struct {
	m    *mim.Model
	c    *clip.Model
	turn one
}

// OpenPhotos loads a Mimir Model file of a CLIP model. It fails with
// ErrBadWeights for a file that is not one or that mimir cannot run.
func OpenPhotos(b []byte) (*Photos, error) {
	m, md, err := open(b)
	if err != nil {
		return nil, err
	}
	c, err := clip.Load(m, md, tensor.DefaultDevice())
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	return &Photos{m: m, c: c, turn: make(one, 1)}, nil
}

// Close releases the model's file.
func (p *Photos) Close() error { return p.m.Close() }

// Matched is how well photos and texts match: Similarity[i][j] is the
// cosine similarity of photo i and text j, and Chance[i][j] the share of
// photo i's match that text j takes among the texts.
type Matched struct {
	Similarity [][]float64
	Chance     [][]float64
}

// Match embeds photos and texts and scores each pair.
func (p *Photos) Match(ctx context.Context, photos []image.Image, texts []string) (Matched, error) {
	if err := p.turn.take(ctx); err != nil {
		return Matched{}, err
	}
	defer p.turn.give()
	// Photos go through the image encoder a few at a time, so a long list
	// doesn't hold all their activations at once.
	const batch = 16
	var ie [][]float32
	for lo := 0; lo < len(photos); lo += batch {
		e, err := p.c.EmbedImages(ctx, photos[lo:min(lo+batch, len(photos))])
		if err != nil {
			return Matched{}, err
		}
		ie = append(ie, e...)
	}
	te, err := p.c.EmbedTexts(ctx, texts)
	if err != nil {
		return Matched{}, err
	}
	out := Matched{Similarity: make([][]float64, len(ie)), Chance: make([][]float64, len(ie))}
	for i, e := range ie {
		out.Similarity[i] = make([]float64, len(te))
		for j, t := range te {
			out.Similarity[i][j] = clip.Similarity(e, t)
		}
		out.Chance[i] = p.c.Scores(e, te)
	}
	return out, nil
}

// Transcribe writes down the speech in wav with version ver of the speech
// model with id, loading it with load on first use.
func (c *Cache) Transcribe(ctx context.Context, id, ver string, load Loader, wav []byte, h Hearing) (Heard, error) {
	e, err := c.media(id, ver, load)
	if err != nil {
		return Heard{}, err
	}
	defer c.release(e)
	if e.speech == nil {
		return Heard{}, fmt.Errorf("%w: only a speech-to-text model writes down recordings", ErrBadInput)
	}
	return e.speech.Transcribe(ctx, wav, h)
}

// MatchPhotos scores photos against texts with version ver of the photo
// search model with id, loading it with load on first use.
func (c *Cache) MatchPhotos(ctx context.Context, id, ver string, load Loader, photos []image.Image, texts []string) (Matched, error) {
	e, err := c.media(id, ver, load)
	if err != nil {
		return Matched{}, err
	}
	defer c.release(e)
	if e.photos == nil {
		return Matched{}, fmt.Errorf("%w: only a photo search model matches photos and words", ErrBadInput)
	}
	return e.photos.Match(ctx, photos, texts)
}

// media returns version ver of the speech or photo model with id, held
// for the caller, loading it on first use by what its file holds.
func (c *Cache) media(id, ver string, load Loader) (*entry, error) {
	return c.get(id, ver, load, func(e *entry, w []byte) {
		start := time.Now()
		switch MediaKind(w) {
		case "whisper":
			e.kind = "speech"
			if e.speech, e.err = OpenSpeech(w); e.err == nil {
				e.closer = e.speech
			}
		case "clip":
			e.kind = "photos"
			if e.photos, e.err = OpenPhotos(w); e.err == nil {
				e.closer = e.photos
			}
		default:
			e.kind = "file"
			e.err = fmt.Errorf("%w: neither a speech-to-text nor a photo search model", ErrBadWeights)
		}
		if o := c.observerNow(); o != nil {
			o.ObserveLoad(e.kind, time.Since(start), e.err != nil)
		}
	})
}
