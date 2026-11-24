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

package update

import (
	"strconv"
	"strings"
)

// Newer reports whether version a is newer than version b. Both are
// release versions such as "0.4.1" or "v0.5.0-rc.1"; a leading "v" is
// ignored. A version that isn't one, such as "dev", is never newer and
// everything is newer than it.
func Newer(a, b string) bool {
	va, oka := parse(a)
	vb, okb := parse(b)
	switch {
	case !oka:
		return false
	case !okb:
		return true
	}
	for i := range 3 {
		if va.nums[i] != vb.nums[i] {
			return va.nums[i] > vb.nums[i]
		}
	}
	// 1.0.0 is newer than 1.0.0-rc.1.
	switch {
	case va.pre == vb.pre:
		return false
	case va.pre == "":
		return true
	case vb.pre == "":
		return false
	}
	return comparePre(va.pre, vb.pre) > 0
}

type semver struct {
	nums [3]int
	pre  string
}

func parse(v string) (semver, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "+") // build metadata doesn't count
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var s semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		s.nums[i] = n
	}
	s.pre = pre
	return s, true
}

// comparePre compares pre-release parts the way semver does: numbers by
// value, words by text, numbers before words, and more parts after fewer.
func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		na, ea := strconv.Atoi(as[i])
		nb, eb := strconv.Atoi(bs[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return cmpInt(na, nb)
			}
		case ea == nil:
			return -1
		case eb == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(as), len(bs))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// IsRelease says whether v is a release version rather than a development
// build.
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}
