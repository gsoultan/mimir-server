package tables

import "github.com/gsoultan/storm"

// Usage counts predictions per month, person and key ("" for the web app).
type Usage struct {
	Month       string
	User        User
	KeyID       string
	KeyName     string
	Predictions int64
}

// Joins: counters with their owner's name, busiest first.
func (u *Usage) Joins(j *storm.Joins) {
	var p User
	j.Named("WithUser").
		Inner(&p, &u.User).
		Take(&u.Month, "Month").
		Take(&u.User, "UserID").
		Take(&u.KeyID, "KeyID").
		Take(&u.KeyName, "KeyName").
		Take(&u.Predictions, "Predictions").
		Take(&p.Name, "UserName").
		Take(&p.Email, "Email").
		OrderDesc(&u.Predictions).
		OrderAsc(&p.Email)
}

func (u *Usage) Schema(t *storm.Table) {
	t.Name("usage")
	// The key serves a month's counters, and one person's in a month.
	t.PrimaryKey(&u.Month, &u.User, &u.KeyID)
	t.Col(&u.User).OnDelete(storm.Cascade) // indexed, for the cascade
	t.Col(&u.KeyID).Default("''")
	t.Col(&u.KeyName).Default("''")
	t.Col(&u.Predictions).Default("0")
	t.Check(storm.RawSQL(`length(month) = 7`))
}
