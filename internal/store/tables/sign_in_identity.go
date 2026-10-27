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
