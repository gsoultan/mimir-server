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

package arch

// What the models of package mimir's audio/whisper and vision/clip read
// and answer. Both are whole-model files, converted on upload from a ZIP
// of a Hugging Face folder or made by mim-import, and run on the console
// itself, loaded on first use, like language models.
const (
	// Recording is speech to write down: a WAV file of any length.
	Recording Input = "recording"
	// PhotosAndWords are photos and words to match against each other.
	PhotosAndWords Input = "photos_words"

	// Transcript is the words said in a recording, with when each part
	// was said.
	Transcript Output = "transcript"
	// Matches say how well each photo fits each text.
	Matches Output = "matches"
)

// Names of the architectures below.
const (
	SpeechToText = "speech-to-text"
	PhotoSearch  = "photo-search"
)

var mediaModels = []Architecture{
	{
		Name:        SpeechToText,
		Title:       "Speech to text",
		Description: "Writes down what people say in a recording, such as a call or a meeting, in about 100 languages, or translates it into English. Upload a ZIP of a Whisper model folder from Hugging Face, such as openai/whisper-small.",
		Input:       Recording,
		Output:      Transcript,
		File:        true,
	},
	{
		Name:        PhotoSearch,
		Title:       "Photo search by words",
		Description: "Finds the photos that fit a few words, such as \"a red dress\" or \"a cracked screen\", and sorts photos into labels you name, without teaching it first. Upload a ZIP of a CLIP model folder from Hugging Face, such as openai/clip-vit-base-patch32.",
		Input:       PhotosAndWords,
		Output:      Matches,
		File:        true,
	},
}

func init() { builtin = append(builtin, mediaModels...) }

// OnConsole reports whether the architecture's models run on the console
// itself, loaded on first use, rather than on workers or in a browser:
// language models, speech to text and photo search.
func (a Architecture) OnConsole() bool {
	switch a.Answers() {
	case Written, Transcript, Matches:
		return true
	}
	return false
}
