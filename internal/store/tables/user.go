package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// User is an account. Its team is a membership, not ownership: people move
// between teams, so unlike TeamOwned the column can be updated.
type User struct {
	Record
	Named
	Email        string
	PasswordHash string
	Role         Role
	AllModels    bool
	Disabled     bool
	LastSignIn   *time.Time
	// TOTPSecret is the confirmed authenticator key; TOTPPending one shown
	// but not yet confirmed. TOTPLastStep refuses a code used before.
	TOTPSecret   string
	TOTPPending  string
	TOTPLastStep int64
	Team         *Team
}

// Aggregates: how many people each team has.
func (u *User) Aggregates(a *storm.Aggregates) {
	perTeam := a.Named("PerTeam")
	perTeam.By(&u.Team)
	perTeam.Count("N")
}

func (u *User) Schema(t *storm.Table) {
	t.Col(&u.Email).Unique()
	t.Col(&u.Disabled).Default("false")
	t.Col(&u.TOTPSecret).Named("totp_secret").Default("''")
	t.Col(&u.TOTPPending).Named("totp_pending").Default("''")
	t.Col(&u.TOTPLastStep).Named("totp_last_step").Default("0")
	// A team's people, in the order they joined; also the key's index.
	t.Col(&u.Team).NoIndex()
	t.Index(&u.Team, &u.CreateTime, &u.ID)
	t.Check(storm.RawSQL(`length(email) BETWEEN 3 AND 254`))
	t.Check(storm.RawSQL(`password_hash <> ''`))
	t.Check(storm.RawSQL(between("role", RoleViewer, RoleOwner)))
}
