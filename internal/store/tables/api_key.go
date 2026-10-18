package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// APIKey is a credential for apps, owned by a user.
type APIKey struct {
	Record
	Named
	User       User
	Prefix     string
	SecretHash string
	Role       Role
	AllModels  bool
	LastUsed   *time.Time
}

func (k *APIKey) Schema(t *storm.Table) {
	t.Col(&k.SecretHash).Unique() // every API call's lookup
	t.Col(&k.Prefix).Immutable()
	// A user's keys, newest first; also the user key's index.
	t.Col(&k.User).OnDelete(storm.Cascade).Immutable().NoIndex()
	t.Index(&k.User, storm.Desc(&k.CreateTime), storm.Desc(&k.ID))
	t.Check(storm.RawSQL(between("role", RoleViewer, RoleOwner)))
}
