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

package training

import (
	"cmp"
	"slices"

	"github.com/gsoultan/mimir/vision"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// boxReport scores a model that finds things in photos on the photos kept
// aside: found is what it found in each, truth what is really there. Nil
// when no photo was kept aside.
func boxReport(found, truth [][]vision.Detection, labels []string) *store.Report {
	if len(truth) == 0 {
		return nil
	}
	k := len(labels)
	r := &store.Report{
		Kind: "boxes", Examples: len(truth),
		AP50: vision.AveragePrecision(found, truth, k, 0.5),
		AP75: vision.AveragePrecision(found, truth, k, 0.75),
	}
	for c, label := range labels {
		f, t := only(found, c), only(truth, c)
		cs := store.ClassScore{Label: label, AP: vision.AveragePrecision(f, t, 1, 0.5)}
		tp, guessed, real := matched(f, t, arch.FindThreshold, 0.5)
		cs.Support = int64(real)
		if guessed > 0 {
			cs.Precision = float64(tp) / float64(guessed)
		}
		if real > 0 {
			cs.Recall = float64(tp) / float64(real)
		}
		if cs.Precision+cs.Recall > 0 {
			cs.F1 = 2 * cs.Precision * cs.Recall / (cs.Precision + cs.Recall)
		}
		if real > 0 || guessed > 0 {
			r.Classes = append(r.Classes, cs)
		}
	}
	return r
}

// only keeps the detections of class c in each photo, as class 0.
func only(photos [][]vision.Detection, c int) [][]vision.Detection {
	out := make([][]vision.Detection, len(photos))
	for p, ds := range photos {
		for _, d := range ds {
			if d.Class == c {
				d.Class = 0
				out[p] = append(out[p], d)
			}
		}
	}
	return out
}

// matched counts, at the least score least, the things found that match a
// real one by at least iou, surest first and each real one once; how many
// it found; and how many are really there.
func matched(found, truth [][]vision.Detection, least, iou float64) (tp, guessed, real int) {
	for p := range truth {
		real += len(truth[p])
		var ds []vision.Detection
		if p < len(found) {
			for _, d := range found[p] {
				if d.Score >= least {
					ds = append(ds, d)
				}
			}
		}
		guessed += len(ds)
		slices.SortStableFunc(ds, func(a, b vision.Detection) int { return cmp.Compare(b.Score, a.Score) })
		used := make([]bool, len(truth[p]))
		for _, d := range ds {
			best, at := iou, -1
			for j, t := range truth[p] {
				if v := vision.IoU(d.Box, t.Box); !used[j] && v >= best {
					best, at = v, j
				}
			}
			if at >= 0 {
				used[at] = true
				tp++
			}
		}
	}
	return tp, guessed, real
}
