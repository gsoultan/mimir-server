package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db"
	"github.com/gsoultan/mimir-server/internal/store/db/apikey"
	"github.com/gsoultan/mimir-server/internal/store/db/apikeymodel"
	"github.com/gsoultan/mimir-server/internal/store/db/auditevent"
	"github.com/gsoultan/mimir-server/internal/store/db/recoverycode"
	"github.com/gsoultan/mimir-server/internal/store/db/session"
	"github.com/gsoultan/mimir-server/internal/store/db/user"
	"github.com/gsoultan/mimir-server/internal/store/db/usermodel"
	"github.com/gsoultan/mimir-server/internal/store/tables"
)

// Role is what a user or key may do. Each role includes the ones below it.
type Role int

const (
	Viewer Role = 1 + iota
	Editor
	Admin
	Owner
)

// Access is a role and the models it applies to. Admins and owners always
// reach every model.
type Access struct {
	Role      Role
	AllModels bool
	ModelIDs  []string // when !AllModels
}

// Allows reports whether access covers the model.
func (a Access) Allows(modelID string) bool {
	if a.AllModels {
		return true
	}
	for _, id := range a.ModelIDs {
		if id == modelID {
			return true
		}
	}
	return false
}

// User is an account.
type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	Access       Access
	Disabled     bool
	Created      time.Time
	LastSignIn   time.Time // zero if never
	// TOTPSecret is set once an authenticator app is confirmed. TOTPPending
	// is a secret shown to the user that no code has confirmed yet.
	TOTPSecret   string
	TOTPPending  string
	TOTPLastStep int64
	// TeamID is the team the person belongs to; they see only its things.
	TeamID string
}

// TwoFactor reports whether sign-in needs a code from an authenticator.
func (u User) TwoFactor() bool { return u.TOTPSecret != "" }

// Session is one signed-in browser.
type Session struct {
	ID       string
	UserID   string
	Device   string
	IP       string
	Pending  bool // waiting for the second factor
	Created  time.Time
	LastSeen time.Time
	Expires  time.Time
}

// APIKey is a credential for apps, owned by a user.
type APIKey struct {
	ID       string
	UserID   string
	Name     string
	Prefix   string
	Access   Access
	Created  time.Time
	LastUsed time.Time // zero if never
}

// AuditEvent is one line in the record of who did what.
type AuditEvent struct {
	ID     string
	Time   time.Time
	Actor  string
	Action string
	Target string
	Detail string
	// TeamID is the team the event happened in, "" for none.
	TeamID   string
	TeamName string // filled in by ListAuditEvents
}

// ErrEmailTaken reports a second user with the same email.
var ErrEmailTaken = errors.New("store: email already in use")

// grantLimit bounds how many model grants one user or key reads back.
const grantLimit = 100000

func newID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("store: new id: %w", err)
	}
	return id.String(), nil
}

func isUnique(err error) bool { return errors.Is(err, runtime.ErrUniqueViolation) }

// CountUsers returns how many users exist, so the server knows whether
// setup is still needed.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	n, err := user.New().Count(ctx, s.ex)
	return int(n), err
}

func fromUser(r user.Row) User {
	return User{
		ID:           r.ID,
		Email:        r.Email,
		Name:         r.Name,
		PasswordHash: r.PasswordHash,
		Access:       Access{Role: Role(r.Role), AllModels: r.AllModels},
		Disabled:     r.Disabled,
		Created:      r.CreateTime.UTC(),
		LastSignIn:   timeOf(r.LastSignIn),
		TOTPSecret:   r.TotpSecret,
		TOTPPending:  r.TotpPending,
		TOTPLastStep: r.TotpLastStep,
		TeamID:       r.TeamID.V,
	}
}

// CreateUser adds a user. It fills in ID and Created.
func (s *Store) CreateUser(ctx context.Context, u User) (User, error) {
	id, err := newID()
	if err != nil {
		return User{}, err
	}
	u.ID, u.Created = id, now()
	u.Access = normalize(u.Access)
	if u.TeamID == "" {
		u.TeamID = DefaultTeam
	}
	err = s.inTx(ctx, func(ex runtime.Executor) error {
		n := user.Create()
		n.SetID(u.ID)
		n.SetEmail(u.Email)
		n.SetName(u.Name)
		n.SetPasswordHash(u.PasswordHash)
		n.SetRole(int32(u.Access.Role))
		n.SetAllModels(u.Access.AllModels)
		n.SetDisabled(u.Disabled)
		n.SetCreateTime(u.Created)
		n.SetTeamID(u.TeamID)
		if _, err := n.Insert(ctx, ex); err != nil {
			if isUnique(err) {
				return ErrEmailTaken
			}
			return fmt.Errorf("store: insert user: %w", err)
		}
		return setUserModels(ctx, ex, u.ID, u.Access, false)
	})
	if err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, u.ID)
}

// GetUser returns a user by id.
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	return s.getUser(ctx, user.New().Where(user.ID.Eq(id)))
}

// GetUserByEmail returns a user by email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	return s.getUser(ctx, user.New().Where(user.Email.Eq(email)))
}

func (s *Store) getUser(ctx context.Context, q user.Query) (User, error) {
	r, ok, err := q.One(ctx, s.ex)
	if err != nil {
		return User{}, fmt.Errorf("store: get user: %w", err)
	}
	if !ok {
		return User{}, ErrNotFound
	}
	u := fromUser(r)
	grants, err := s.userGrants(ctx, u.ID)
	if err != nil {
		return User{}, err
	}
	u.Access.ModelIDs = grants[u.ID]
	return u, nil
}

// ListUsers returns a team's users, or everyone's when teamID is "",
// oldest first.
func (s *Store) ListUsers(ctx context.Context, teamID string) ([]User, error) {
	rows, err := user.New().
		WhereIf(teamID != "", user.TeamID.Eq(teamID)).
		Order(user.CreateTime.Asc(), user.ID.Asc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	grants, err := s.userGrants(ctx, ids...)
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		u := fromUser(r)
		u.Access.ModelIDs = grants[u.ID]
		out = append(out, u)
	}
	return out, nil
}

// userGrants reads the model grants of several users in one query.
func (s *Store) userGrants(ctx context.Context, userIDs ...string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := usermodel.New().
		Where(usermodel.UserID.In(userIDs...)).
		Order(usermodel.UserID.Asc(), usermodel.ModelID.Asc()).
		Limit(grantLimit).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: user_models: %w", err)
	}
	for _, r := range rows {
		out[r.UserID] = append(out[r.UserID], r.ModelID)
	}
	return out, nil
}

// keyGrants reads the model grants of several API keys in one query.
func (s *Store) keyGrants(ctx context.Context, keyIDs ...string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(keyIDs) == 0 {
		return out, nil
	}
	rows, err := apikeymodel.New().
		Where(apikeymodel.KeyID.In(keyIDs...)).
		Order(apikeymodel.KeyID.Asc(), apikeymodel.ModelID.Asc()).
		Limit(grantLimit).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: api_key_models: %w", err)
	}
	for _, r := range rows {
		out[r.KeyID] = append(out[r.KeyID], r.ModelID)
	}
	return out, nil
}

// MoveUser puts a user in another team. Their model grants go, their API
// keys are revoked and their sessions end except keepSession, since all
// were for the old team's models.
func (s *Store) MoveUser(ctx context.Context, id, teamID, keepSession string) error {
	return s.inTx(ctx, func(ex runtime.Executor) error {
		m := user.MutateKey(id)
		m.SetTeamID(teamID)
		if err := m.Update(ctx, ex); err != nil {
			if errors.Is(err, runtime.ErrNoRow) {
				return ErrNotFound
			}
			return fmt.Errorf("store: move user: %w", err)
		}
		if _, err := tables.DeleteUserModels.Exec(ctx, ex, id); err != nil {
			return fmt.Errorf("store: move user: %w", err)
		}
		if _, err := tables.DeleteUserAPIKeys.Exec(ctx, ex, id); err != nil {
			return fmt.Errorf("store: move user: %w", err)
		}
		if _, err := tables.DeleteOtherSessions.Exec(ctx, ex, id, keepSession); err != nil {
			return fmt.Errorf("store: move user: %w", err)
		}
		return nil
	})
}

// UpdateUser changes a user's name, access and disabled flag.
func (s *Store) UpdateUser(ctx context.Context, id, name string, access Access, disabled bool) (User, error) {
	access = normalize(access)
	err := s.inTx(ctx, func(ex runtime.Executor) error {
		m := user.MutateKey(id)
		m.SetName(name)
		m.SetRole(int32(access.Role))
		m.SetAllModels(access.AllModels)
		m.SetDisabled(disabled)
		if err := m.Update(ctx, ex); err != nil {
			if errors.Is(err, runtime.ErrNoRow) {
				return ErrNotFound
			}
			return fmt.Errorf("store: update user: %w", err)
		}
		return setUserModels(ctx, ex, id, access, true)
	})
	if err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, id)
}

// SetPassword replaces a user's password hash and ends all their sessions.
func (s *Store) SetPassword(ctx context.Context, id, hash string) error {
	return s.inTx(ctx, func(ex runtime.Executor) error {
		m := user.MutateKey(id)
		m.SetPasswordHash(hash)
		if err := m.Update(ctx, ex); err != nil {
			if errors.Is(err, runtime.ErrNoRow) {
				return ErrNotFound
			}
			return fmt.Errorf("store: set password: %w", err)
		}
		if _, err := tables.DeleteUserSessions.Exec(ctx, ex, id); err != nil {
			return fmt.Errorf("store: end sessions: %w", err)
		}
		return nil
	})
}

// DeleteUser removes a user with their sessions and keys.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	if err := user.Delete(ctx, s.ex, id); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete user: %w", err)
	}
	return nil
}

// CountOwners returns how many enabled owners exist.
func (s *Store) CountOwners(ctx context.Context) (int, error) {
	n, err := user.New().Where(user.Role.Eq(int32(Owner)), user.Disabled.Eq(false)).Count(ctx, s.ex)
	return int(n), err
}

// Session tokens are stored hashed; a leaked database can't sign anyone in.

// CreateSession records a session and returns its id. A pending session
// is a sign-in that still needs its second factor.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, in Session) (Session, error) {
	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	t := now()
	in.ID, in.Created, in.LastSeen = id, t, t
	n := session.Create()
	n.SetID(id)
	n.SetTokenHash(tokenHash)
	n.SetUserID(in.UserID)
	n.SetAgentHash(agentHash(in.Device))
	n.SetDevice(in.Device)
	n.SetIP(in.IP)
	n.SetPending(in.Pending)
	n.SetCreateTime(t)
	n.SetLastSeen(t)
	n.SetExpireTime(ms(in.Expires))
	if _, err := n.Insert(ctx, s.ex); err != nil {
		return Session{}, fmt.Errorf("store: create session: %w", err)
	}
	if !in.Pending {
		m := user.MutateKey(in.UserID)
		m.SetLastSignIn(t)
		if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
			return in, err
		}
	}
	return in, nil
}

// agentHash is what binds a session to one browser. The description is
// what the browser sends in User-Agent, reduced by the caller.
func agentHash(device string) string {
	sum := sha256.Sum256([]byte(device))
	return hex.EncodeToString(sum[:8])
}

// ErrWrongDevice means a session token arrived from a browser other than
// the one that signed in: either a stolen cookie or a changed browser.
var ErrWrongDevice = errors.New("store: session used from another browser")

// SessionIdleTTL ends a session nobody has used for a while, even before
// its absolute expiry.
const SessionIdleTTL = 7 * 24 * time.Hour

func fromSession(r session.Row) Session {
	return Session{
		ID:       r.ID,
		UserID:   r.UserID,
		Device:   r.Device,
		IP:       r.IP,
		Pending:  r.Pending,
		Created:  r.CreateTime.UTC(),
		LastSeen: r.LastSeen.UTC(),
		Expires:  r.ExpireTime.UTC(),
	}
}

// LookupSession returns the session a live token belongs to and its user,
// bumping last_seen. device must match the one that created it.
func (s *Store) LookupSession(ctx context.Context, tokenHash, device string) (Session, User, error) {
	r, ok, err := session.New().Where(session.TokenHash.Eq(tokenHash)).One(ctx, s.ex)
	if err != nil {
		return Session{}, User{}, fmt.Errorf("store: session: %w", err)
	}
	if !ok {
		return Session{}, User{}, ErrNotFound
	}
	se := fromSession(r)
	t := time.Now()
	if !t.Before(se.Expires) || (!se.Pending && t.Sub(se.LastSeen) > SessionIdleTTL) {
		session.Delete(ctx, s.ex, se.ID)
		return Session{}, User{}, ErrNotFound
	}
	if r.AgentHash != agentHash(device) {
		return Session{}, User{}, ErrWrongDevice
	}
	// Write last_seen at most once a minute; it's a hint, not a ledger.
	if t.Sub(se.LastSeen) > time.Minute {
		se.LastSeen = ms(t)
		m := session.MutateKey(se.ID)
		m.SetLastSeen(se.LastSeen)
		m.Update(ctx, s.ex)
	}
	u, err := s.GetUser(ctx, se.UserID)
	return se, u, err
}

// ListSessions returns a user's live sessions, newest first.
func (s *Store) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	t := time.Now()
	rows, err := session.New().
		Where(
			session.UserID.Eq(userID),
			session.Pending.Eq(false),
			session.ExpireTime.Gt(t),
			session.LastSeen.Gt(t.Add(-SessionIdleTTL)),
		).
		Order(session.LastSeen.Desc(), session.ID.Desc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSession(r))
	}
	return out, nil
}

// DeleteSession ends one session by its token.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	r, ok, err := session.New().Where(session.TokenHash.Eq(tokenHash)).One(ctx, s.ex)
	if err != nil || !ok {
		return err
	}
	if err := session.Delete(ctx, s.ex, r.ID); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return err
	}
	return nil
}

// RevokeSession ends one of a user's sessions by id.
func (s *Store) RevokeSession(ctx context.Context, userID, id string) (bool, error) {
	ok, err := session.New().Where(session.ID.Eq(id), session.UserID.Eq(userID)).Exists(ctx, s.ex)
	if err != nil || !ok {
		return false, err
	}
	err = session.Delete(ctx, s.ex, id)
	if errors.Is(err, runtime.ErrNoRow) {
		return false, nil
	}
	return err == nil, err
}

// RevokeOtherSessions ends every session of a user except keep.
func (s *Store) RevokeOtherSessions(ctx context.Context, userID, keep string) (int64, error) {
	return tables.DeleteOtherSessions.Exec(ctx, s.ex, userID, keep)
}

// EndSessions signs a user out everywhere.
func (s *Store) EndSessions(ctx context.Context, userID string) error {
	_, err := tables.DeleteUserSessions.Exec(ctx, s.ex, userID)
	return err
}

// ExpireSessions drops sessions past their end or idle for too long.
func (s *Store) ExpireSessions(ctx context.Context) error {
	t := time.Now()
	_, err := tables.DeleteStaleSessions.Exec(ctx, s.ex, t, t.Add(-SessionIdleTTL))
	return err
}

// Two-step verification

// SetPendingTOTP stores a secret the user has been shown but not confirmed.
func (s *Store) SetPendingTOTP(ctx context.Context, userID, secret string) error {
	m := user.MutateKey(userID)
	m.SetTotpPending(secret)
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return err
	}
	return nil
}

// EnableTOTP promotes the pending secret, stores hashed recovery codes and
// ends every other session, so a hijacked one can't ride along.
func (s *Store) EnableTOTP(ctx context.Context, userID, keepSession string, codeHashes []string) error {
	return s.inTx(ctx, func(ex runtime.Executor) error {
		r, ok, err := user.New().Where(user.ID.Eq(userID)).ForUpdate().One(ctx, ex)
		if err != nil {
			return fmt.Errorf("store: enable totp: %w", err)
		}
		if ok && r.TotpPending != "" {
			m := user.Mutate(r)
			m.SetTotpSecret(r.TotpPending)
			m.SetTotpPending("")
			m.SetTotpLastStep(0)
			if err := m.Update(ctx, ex); err != nil {
				return fmt.Errorf("store: enable totp: %w", err)
			}
		}
		if err := replaceRecoveryCodes(ctx, ex, userID, codeHashes); err != nil {
			return err
		}
		_, err = tables.DeleteOtherSessions.Exec(ctx, ex, userID, keepSession)
		return err
	})
}

// DisableTOTP turns two-step verification off and drops recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, userID string) error {
	return s.inTx(ctx, func(ex runtime.Executor) error {
		m := user.MutateKey(userID)
		m.SetTotpSecret("")
		m.SetTotpPending("")
		m.SetTotpLastStep(0)
		if err := m.Update(ctx, ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
			return fmt.Errorf("store: disable totp: %w", err)
		}
		return replaceRecoveryCodes(ctx, ex, userID, nil)
	})
}

func replaceRecoveryCodes(ctx context.Context, ex runtime.Executor, userID string, hashes []string) error {
	if _, err := tables.DeleteRecoveryCodes.Exec(ctx, ex, userID); err != nil {
		return err
	}
	if len(hashes) == 0 {
		return nil
	}
	ops := make([]runtime.BatchOp, 0, len(hashes))
	for _, h := range hashes {
		ops = append(ops, recoverycode.InsertOp(recoverycode.Row{UserID: userID, CodeHash: h}))
	}
	if err := ex.Batch(ctx, ops, nil); err != nil {
		return fmt.Errorf("store: recovery code: %w", err)
	}
	return nil
}

// UseTOTPStep records that a code for step was accepted, refusing a replay.
// It reports whether the step was new.
func (s *Store) UseTOTPStep(ctx context.Context, userID string, step int64) (bool, error) {
	fresh := false
	err := s.inTx(ctx, func(ex runtime.Executor) error {
		// The row lock makes the check and the write one step: two requests
		// with the same code can't both see the old step.
		r, ok, err := user.New().Where(user.ID.Eq(userID)).ForUpdate().One(ctx, ex)
		if err != nil || !ok || r.TotpLastStep >= step {
			return err
		}
		m := user.Mutate(r)
		m.SetTotpLastStep(step)
		if err := m.Update(ctx, ex); err != nil {
			return err
		}
		fresh = true
		return nil
	})
	return fresh && err == nil, err
}

// UseRecoveryCode spends a recovery code and returns how many remain.
func (s *Store) UseRecoveryCode(ctx context.Context, userID, codeHash string) (bool, int, error) {
	used := true
	if err := recoverycode.Delete(ctx, s.ex, userID, codeHash); err != nil {
		if !errors.Is(err, runtime.ErrNoRow) {
			return false, 0, err
		}
		used = false
	}
	left, err := recoverycode.New().Where(recoverycode.UserID.Eq(userID)).Count(ctx, s.ex)
	return used, int(left), err
}

func fromKey(r apikey.Row) APIKey {
	return APIKey{
		ID:       r.ID,
		UserID:   r.UserID,
		Name:     r.Name,
		Prefix:   r.Prefix,
		Access:   Access{Role: Role(r.Role), AllModels: r.AllModels},
		Created:  r.CreateTime.UTC(),
		LastUsed: timeOf(r.LastUsed),
	}
}

// CreateAPIKey stores a key whose secret hashes to secretHash.
func (s *Store) CreateAPIKey(ctx context.Context, k APIKey, secretHash string) (APIKey, error) {
	id, err := newID()
	if err != nil {
		return APIKey{}, err
	}
	k.ID, k.Created = id, now()
	k.Access = normalize(k.Access)
	err = s.inTx(ctx, func(ex runtime.Executor) error {
		n := apikey.Create()
		n.SetID(k.ID)
		n.SetUserID(k.UserID)
		n.SetName(k.Name)
		n.SetPrefix(k.Prefix)
		n.SetSecretHash(secretHash)
		n.SetRole(int32(k.Access.Role))
		n.SetAllModels(k.Access.AllModels)
		n.SetCreateTime(k.Created)
		if _, err := n.Insert(ctx, ex); err != nil {
			return fmt.Errorf("store: insert key: %w", err)
		}
		ops := make([]runtime.BatchOp, 0, len(k.Access.ModelIDs))
		for _, m := range k.Access.ModelIDs {
			g := apikeymodel.Create()
			g.SetKeyID(k.ID)
			g.SetModelID(m)
			op, err := g.DoNothing().Op()
			if err != nil {
				return err
			}
			ops = append(ops, op)
		}
		if len(ops) > 0 {
			if err := ex.Batch(ctx, ops, nil); err != nil {
				return fmt.Errorf("store: grant model: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return APIKey{}, err
	}
	return k, nil
}

// ListAPIKeys returns a user's keys, newest first.
func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	rows, err := apikey.New().
		Where(apikey.UserID.Eq(userID)).
		Order(apikey.CreateTime.Desc(), apikey.ID.Desc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list keys: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	grants, err := s.keyGrants(ctx, ids...)
	if err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(rows))
	for _, r := range rows {
		k := fromKey(r)
		k.Access.ModelIDs = grants[k.ID]
		out = append(out, k)
	}
	return out, nil
}

// KeyUser returns the key with secretHash and its owner, and notes the use.
func (s *Store) KeyUser(ctx context.Context, secretHash string) (APIKey, User, error) {
	r, ok, err := apikey.New().Where(apikey.SecretHash.Eq(secretHash)).One(ctx, s.ex)
	if err != nil {
		return APIKey{}, User{}, fmt.Errorf("store: key: %w", err)
	}
	if !ok {
		return APIKey{}, User{}, ErrNotFound
	}
	k := fromKey(r)
	grants, err := s.keyGrants(ctx, k.ID)
	if err != nil {
		return APIKey{}, User{}, err
	}
	k.Access.ModelIDs = grants[k.ID]
	u, err := s.GetUser(ctx, k.UserID)
	if err != nil {
		return APIKey{}, User{}, err
	}
	// Best effort; a failed timestamp shouldn't fail the request.
	m := apikey.MutateKey(k.ID)
	m.SetLastUsed(now())
	m.Update(ctx, s.ex)
	return k, u, nil
}

// DeleteAPIKey removes one of userID's keys.
func (s *Store) DeleteAPIKey(ctx context.Context, userID, id string) error {
	ok, err := apikey.New().Where(apikey.ID.Eq(id), apikey.UserID.Eq(userID)).Exists(ctx, s.ex)
	if err != nil {
		return fmt.Errorf("store: delete key: %w", err)
	}
	if !ok {
		return ErrNotFound
	}
	if err := apikey.Delete(ctx, s.ex, id); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete key: %w", err)
	}
	return nil
}

// Audit appends an event.
func (s *Store) Audit(ctx context.Context, e AuditEvent) error {
	id, err := newID()
	if err != nil {
		return err
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	n := auditevent.Create()
	n.SetID(id)
	n.SetTime(ms(e.Time))
	n.SetActor(e.Actor)
	n.SetAction(e.Action)
	n.SetTarget(e.Target)
	n.SetDetail(e.Detail)
	if e.TeamID != "" {
		n.SetTeamID(e.TeamID)
	}
	if _, err := n.Insert(ctx, s.ex); err != nil {
		return fmt.Errorf("store: audit: %w", err)
	}
	return nil
}

// ListAuditEvents returns up to limit of a team's events (every team's
// when teamID is "") older than the one with beforeID (every event when
// empty), newest first.
func (s *Store) ListAuditEvents(ctx context.Context, teamID, beforeID string, limit int) ([]AuditEvent, error) {
	rows, err := db.AuditEventWithTeam().
		WhereIf(teamID != "", auditevent.TeamID.Eq(teamID)).
		WhereIf(beforeID != "", auditevent.ID.Lt(beforeID)).
		Order(auditevent.ID.Desc()).
		Limit(int64(limit)).
		All(ctx, s.ex)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	out := make([]AuditEvent, 0, len(rows))
	for _, r := range rows {
		e := AuditEvent{
			ID:     r.ID,
			Time:   r.Time.UTC(),
			Actor:  r.Actor,
			Action: r.Action,
			Target: r.Target,
			Detail: r.Detail,
			TeamID: r.TeamID.V,
		}
		if r.Team != nil {
			e.TeamName = r.Team.Name
		}
		out = append(out, e)
	}
	return out, nil
}

// normalize makes admins and owners reach every model and drops the model
// list when it doesn't apply.
func normalize(a Access) Access {
	if a.Role >= Admin {
		a.AllModels = true
	}
	if a.AllModels {
		a.ModelIDs = nil
	}
	return a
}

// setUserModels replaces a user's model grants, clearing the old ones
// first when clear is set. A model listed twice is granted once.
func setUserModels(ctx context.Context, ex runtime.Executor, userID string, a Access, clear bool) error {
	if clear {
		if _, err := tables.DeleteUserModels.Exec(ctx, ex, userID); err != nil {
			return fmt.Errorf("store: clear user_models: %w", err)
		}
	}
	ops := make([]runtime.BatchOp, 0, len(a.ModelIDs))
	for _, m := range a.ModelIDs {
		g := usermodel.Create()
		g.SetUserID(userID)
		g.SetModelID(m)
		op, err := g.DoNothing().Op()
		if err != nil {
			return err
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return nil
	}
	if err := ex.Batch(ctx, ops, nil); err != nil {
		return fmt.Errorf("store: grant model: %w", err)
	}
	return nil
}
