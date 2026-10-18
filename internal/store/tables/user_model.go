package tables

import "github.com/gsoultan/storm"

// UserModel grants a user one model when the user's AllModels is false.
type UserModel struct {
	User  User
	Model Model
}

func (g *UserModel) Schema(t *storm.Table) {
	t.PrimaryKey(&g.User, &g.Model)
	t.Col(&g.User).OnDelete(storm.Cascade).NoIndex() // the key's first column
	t.Col(&g.Model).OnDelete(storm.Cascade)          // indexed, for the cascade
}
