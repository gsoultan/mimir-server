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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gsoultan/mimir/tabular"
	"github.com/gsoultan/mimir/tree"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// Up and Down are the ways a rule keeps a model's answer going as its
// number column rises, all else the same.
const (
	Up   = 1
	Down = -1
)

// MaxRules bounds the rules a run may have, and MaxRulesText their
// written form.
const (
	MaxRules     = 50
	MaxRulesText = 4000
)

// ErrBadRules is what ParseRules and Inputs' checks return for rules that
// can't be kept.
var ErrBadRules = errors.New("arch: bad rules")

// Inputs is what a FitColumns learner knows of a table beyond its
// numbers.
type Inputs struct {
	// Columns are the input columns the numbers come from, in order;
	// nil when unknown.
	Columns []schema.Column
	// Rules map number columns, by name, to Up or Down: the model's
	// answer only goes that way as the column rises. For a yes-or-no
	// answer it is the chance of the second label.
	Rules map[string]int
}

// FormatRules writes rules as a JSON object of column names to "up" or
// "down", sorted by name; "" for none.
func FormatRules(rules map[string]int) string {
	if len(rules) == 0 {
		return ""
	}
	out := make(map[string]string, len(rules))
	for c, d := range rules {
		out[c] = Direction(d)
	}
	b, _ := json.Marshal(out) // map keys are sorted
	return string(b)
}

// Direction is "up" for Up and "down" for Down.
func Direction(d int) string {
	if d < 0 {
		return "down"
	}
	return "up"
}

// ParseDirection reads "up" or "down".
func ParseDirection(s string) (int, error) {
	switch s {
	case "up":
		return Up, nil
	case "down":
		return Down, nil
	}
	return 0, fmt.Errorf("%w: the way is up or down, not %q", ErrBadRules, s)
}

// ParseRules reads what FormatRules writes; "" is none.
func ParseRules(s string) (map[string]int, error) {
	if s == "" {
		return nil, nil
	}
	var in map[string]string
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRules, err)
	}
	out := make(map[string]int, len(in))
	for c, d := range in {
		v, err := ParseDirection(d)
		if err != nil {
			return nil, err
		}
		out[c] = v
	}
	return out, nil
}

// CheckRules says whether rules can be kept for a table of cols: each
// names a number column. cols may be nil when they aren't known yet.
func CheckRules(rules map[string]int, cols []schema.Column) error {
	if len(rules) > MaxRules {
		return fmt.Errorf("%w: at most %d columns can have a rule", ErrBadRules, MaxRules)
	}
	if len(FormatRules(rules)) > MaxRulesText {
		return fmt.Errorf("%w: the column names are too long", ErrBadRules)
	}
	for _, c := range slices.Sorted(maps.Keys(rules)) {
		if d := rules[c]; d != Up && d != Down {
			return fmt.Errorf("%w: the way for %s is up or down", ErrBadRules, c)
		}
		if cols == nil {
			continue
		}
		i := slices.IndexFunc(cols, func(x schema.Column) bool { return x.Name == c })
		switch {
		case i < 0:
			return fmt.Errorf("%w: there is no column %s to learn from", ErrBadRules, c)
		case cols[i].Kind != schema.Number:
			return fmt.Errorf("%w: %s isn't a number column, so it has no up or down", ErrBadRules, c)
		}
	}
	return nil
}

// monotone returns the tree option that keeps in's rules on d, read from
// in's columns, or nil when there are none. classes is 0 for a number
// answer; a rule needs a number or one of two answers.
func monotone(in Inputs, d tabular.Data, classes int) ([]tree.Option, error) {
	if len(in.Rules) == 0 {
		return nil, nil
	}
	if classes > 2 {
		return nil, fmt.Errorf("a rule that the answer goes up or down needs a number or a yes-or-no answer, and this one has %d answers", classes)
	}
	if err := CheckRules(in.Rules, in.Columns); err != nil {
		return nil, errors.New(strings.TrimPrefix(err.Error(), ErrBadRules.Error()+": "))
	}
	if in.Columns == nil || schema.Width(in.Columns) != d.Features {
		return nil, errors.New("the columns to learn from aren't known, so the up-or-down rules can't be kept")
	}
	rules := map[int]int{}
	at := 0
	for _, c := range in.Columns {
		if r, ok := in.Rules[c.Name]; ok {
			rules[at] = r
		}
		at += c.Width()
	}
	return []tree.Option{tree.Monotone(rules)}, nil
}
