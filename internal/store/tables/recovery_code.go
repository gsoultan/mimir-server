package tables

import "github.com/gsoultan/storm"

// RecoveryCode is a hashed one-time code for when the phone is gone.
type RecoveryCode struct {
	User     User
	CodeHash string
}

func (r *RecoveryCode) Schema(t *storm.Table) {
	t.PrimaryKey(&r.User, &r.CodeHash)
	t.Col(&r.User).OnDelete(storm.Cascade).NoIndex() // the key's first column
}
