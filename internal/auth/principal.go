package auth

import (
	"context"
	"strings"

	"github.com/gsoultan/mimir-server/internal/store"
)

// Permission names one thing a caller may do.
type Permission string

const (
	ModelsRead     Permission = "models.read"    // list and view models
	ModelsPredict  Permission = "models.predict" // ask models questions
	ModelsWrite    Permission = "models.write"   // add, change and delete models
	UsersRead      Permission = "users.read"
	UsersWrite     Permission = "users.write" // manage viewers and editors
	UsersAdmin     Permission = "users.admin" // manage admins and owners
	AuditRead      Permission = "audit.read"
	KeysManage     Permission = "keys.manage" // the caller's own API keys
	WebhooksManage Permission = "webhooks.manage"
	SettingsManage Permission = "settings.manage"
	WorkersManage  Permission = "workers.manage" // register and remove workers
)

// Permissions returns what a role may do.
func Permissions(r store.Role) []Permission {
	var ps []Permission
	if r >= store.Viewer {
		ps = append(ps, ModelsRead, ModelsPredict, KeysManage)
	}
	if r >= store.Editor {
		ps = append(ps, ModelsWrite)
	}
	if r >= store.Admin {
		ps = append(ps, UsersRead, UsersWrite, AuditRead, WebhooksManage, SettingsManage, WorkersManage)
	}
	if r >= store.Owner {
		ps = append(ps, UsersAdmin)
	}
	return ps
}

// Principal is who is calling and what they may do.
type Principal struct {
	User    store.User
	Key     *store.APIKey  // set when the caller used an API key
	Session *store.Session // set when the caller used a browser session
	Access  store.Access   // effective access: the key's, capped by the user's
}

// Can reports whether the caller has p.
func (p Principal) Can(perm Permission) bool {
	for _, q := range Permissions(p.Access.Role) {
		if q == perm {
			return true
		}
	}
	return false
}

// CanModel reports whether the caller has perm on one model.
func (p Principal) CanModel(perm Permission, modelID string) bool {
	return p.Can(perm) && p.Access.Allows(modelID)
}

// Actor names the caller for the audit log.
func (p Principal) Actor() string {
	if p.Key != nil {
		return "key " + p.Key.Name + " (" + p.User.Email + ")"
	}
	return p.User.Email
}

// Effective returns a key's access capped by its owner's: the lower role
// and only models both reach.
func Effective(user, key store.Access) store.Access {
	out := store.Access{Role: min(user.Role, key.Role)}
	switch {
	case user.AllModels && key.AllModels:
		out.AllModels = true
	case user.AllModels:
		out.ModelIDs = key.ModelIDs
	case key.AllModels:
		out.ModelIDs = user.ModelIDs
	default:
		for _, id := range key.ModelIDs {
			if user.Allows(id) {
				out.ModelIDs = append(out.ModelIDs, id)
			}
		}
	}
	return out
}

type ctxKey struct{}

// WithPrincipal attaches p to ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the caller, if signed in.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// NormalizeEmail lowercases and trims an email address.
func NormalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
