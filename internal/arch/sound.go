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

import (
	"github.com/gsoultan/mimir/audio"
	"github.com/gsoultan/mimir/nn"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// soundNets are the sound architectures built from package audio.
var soundNets = []Architecture{
	{
		Name:        "sound",
		Title:       "Sound classifier",
		Description: "Tells sounds apart, such as a machine running well or badly, a dog barking or a word said: it reads each second of sound as a mel spectrogram, a picture of its pitches over time, with a small convolutional network. It can start from what another sound model learned.",
		Input:       Sound,
		New: func(s Spec) nn.Module {
			mel := audio.NewMelSpectrogram(audio.Mel{Rate: schema.SoundRate})
			return audio.NewClassifier(len(s.Labels), mel, 0.25)
		},
	},
}

func init() { builtin = append(builtin, soundNets...) }
