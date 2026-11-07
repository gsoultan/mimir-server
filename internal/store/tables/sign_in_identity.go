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

package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// SignInIdentity ties an account to a person at an outside sign-in
// provider (Google, GitHub, a company's OpenID Connect), by the issuer and
// the provider's own id for them, which stays put when their email
// changes.
type SignInIdentity struct {
	Record
	User       User
	Issuer     string
	Subject    string
	Email      string
	LastSignIn time.Time
}

func (i *SignInIdentity) Schema(t *storm.Table) {
	t.Col(&i.User).OnDelete(storm.Cascade).Immutable()
	t.Col(&i.Issuer).Immutable()
	t.Col(&i.Subject).Immutable()
	t.Unique(&i.Issuer, &i.Subject) // every sign-in's lookup
	t.Col(&i.Email).Default("''")
	t.Check(storm.RawSQL(`issuer <> '' AND subject <> ''`))
}
