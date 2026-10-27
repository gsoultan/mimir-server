package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/signinidentity"
)

// Identity is a person at an outside sign-in provider, tied to an
// account: the provider's issuer and its own id for them.
type Identity struct {
	Issuer  string
	Subject string
	Email   string
}

// ErrIdentityTaken reports an identity already tied to another account.
var ErrIdentityTaken = errors.New("store: sign-in identity already linked")

// UserByIdentity returns the account an identity is tied to, and notes
// the sign-in and the address the provider gave this time.
func (s *Store) UserByIdentity(ctx context.Context, id Identity) (User, error) {
	r, ok, err := signinidentity.New().Where(signinidentity.Issuer.Eq(id.Issuer), signinidentity.Subject.Eq(id.Subject)).One(ctx, s.ex)
	if err != nil {
		return User{}, fmt.Errorf("store: sign-in identity: %w", err)
	}
	if !ok {
		return User{}, ErrNotFound
	}
	m := signinidentity.MutateKey(r.ID)
	m.SetEmail(id.Email)
	m.SetLastSignIn(now())
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return User{}, fmt.Errorf("store: note sign-in identity: %w", err)
	}
	return s.GetUser(ctx, r.UserID)
}

// LinkIdentity ties an identity to an account.
func (s *Store) LinkIdentity(ctx context.Context, userID string, id Identity) error {
	rid, err := newID()
	if err != nil {
		return err
	}
	t := now()
	n := signinidentity.Create()
	n.SetID(rid)
	n.SetCreateTime(t)
	n.SetUserID(userID)
	n.SetIssuer(id.Issuer)
	n.SetSubject(id.Subject)
	n.SetEmail(id.Email)
	n.SetLastSignIn(t)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		if isUnique(err) {
			return ErrIdentityTaken
		}
		return fmt.Errorf("store: link sign-in identity: %w", err)
	}
	return nil
}
