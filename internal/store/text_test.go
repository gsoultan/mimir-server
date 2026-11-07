// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is dual-licensed: you may use it under the GNU Affero General Public
// License, version 3, as published by the Free Software Foundation, or
// under a commercial license from Gembit Soultan Shirazi. See the LICENSE and
// LICENSING.md files at the root of this repository.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero
// General Public License for more details.

package store

import (
	"errors"
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"  houses ", "houses", true},
		{strings.Repeat("é", MaxName), strings.Repeat("é", MaxName), true},
		{strings.Repeat("a", MaxName+1), "", false},
		{"   ", "", false},
		{"\xff", "", false},
	} {
		n, err := ParseName("model", tc.in)
		if tc.ok != (err == nil) || n.String() != tc.want {
			t.Errorf("ParseName(%q) = %q, %v", tc.in, n, err)
		}
		var inv *InvalidError
		if err != nil && (!errors.As(err, &inv) || !strings.Contains(err.Error(), "model")) {
			t.Errorf("ParseName(%q) refused with %v, want an InvalidError naming the model", tc.in, err)
		}
	}
}

func TestParseDescription(t *testing.T) {
	if d, err := ParseDescription(" "); err != nil || d.String() != "" {
		t.Errorf("an empty description: %q, %v", d, err)
	}
	if _, err := ParseDescription(strings.Repeat("a", MaxDescription+1)); err == nil {
		t.Error("an over-long description was taken")
	}
}
