package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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

func newID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("store: new id: %w", err)
	}
	return id.String(), nil
}

func millis(t time.Time) int64 { return t.UTC().UnixMilli() }

func fromMillis(ms sql.NullInt64) time.Time {
	if !ms.Valid {
		return time.Time{}
	}
	return time.UnixMilli(ms.Int64).UTC()
}

// CountUsers returns how many users exist, so the server knows whether
// setup is still needed.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

const userColumns = "id, email, name, password_hash, role, all_models, disabled, create_time, last_sign_in, totp_secret, totp_pending, totp_last_step, team_id"

func scanUser(row scanner) (User, error) {
	var u User
	var created int64
	var signIn sql.NullInt64
	var team sql.NullString
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Access.Role, &u.Access.AllModels, &u.Disabled, &created, &signIn, &u.TOTPSecret, &u.TOTPPending, &u.TOTPLastStep, &team)
	u.TeamID = team.String
	u.Created = time.UnixMilli(created).UTC()
	u.LastSignIn = fromMillis(signIn)
	return u, err
}

// CreateUser adds a user. It fills in ID and Created.
func (s *Store) CreateUser(ctx context.Context, u User) (User, error) {
	id, err := newID()
	if err != nil {
		return User{}, err
	}
	u.ID, u.Created = id, time.Now().UTC().Truncate(time.Millisecond)
	u.Access = normalize(u.Access)
	if u.TeamID == "" {
		u.TeamID = DefaultTeam
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO users ("+userColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, '', '', 0, ?)",
		u.ID, u.Email, u.Name, u.PasswordHash, u.Access.Role, u.Access.AllModels, u.Disabled, millis(u.Created), u.TeamID)
	if isUnique(err) {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("store: insert user: %w", err)
	}
	if err := setModels(ctx, tx, "user_models", "user_id", u.ID, u.Access); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, u.ID)
}

// GetUser returns a user by id.
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: get user: %w", err)
	}
	u.Access.ModelIDs, err = s.models(ctx, "user_models", "user_id", u.ID)
	return u, err
}

// GetUserByEmail returns a user by email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE email = ?", email))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: get user by email: %w", err)
	}
	u.Access.ModelIDs, err = s.models(ctx, "user_models", "user_id", u.ID)
	return u, err
}

// ListUsers returns a team's users, or everyone's when team is "", oldest
// first.
func (s *Store) ListUsers(ctx context.Context, team string) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userColumns+" FROM users WHERE (? = '' OR team_id = ?) ORDER BY create_time, id LIMIT 1000", team, team)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list users: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Access.ModelIDs, err = s.models(ctx, "user_models", "user_id", out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MoveUser puts a user in another team. Their model grants go, their API
// keys are revoked and their sessions end except keepSession, since all
// were for the old team's models.
func (s *Store) MoveUser(ctx context.Context, id, team, keepSession string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE users SET team_id = ? WHERE id = ?", team, id)
	if err != nil {
		return fmt.Errorf("store: move user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, q := range []string{
		"DELETE FROM user_models WHERE user_id = ?",
		"DELETE FROM api_keys WHERE user_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("store: move user: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id <> ?", id, keepSession); err != nil {
		return fmt.Errorf("store: move user: %w", err)
	}
	return tx.Commit()
}

// UpdateUser changes a user's name, access and disabled flag.
func (s *Store) UpdateUser(ctx context.Context, id, name string, access Access, disabled bool) (User, error) {
	access = normalize(access)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE users SET name = ?, role = ?, all_models = ?, disabled = ? WHERE id = ?",
		name, access.Role, access.AllModels, disabled, id)
	if err != nil {
		return User{}, fmt.Errorf("store: update user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return User{}, ErrNotFound
	}
	if err := setModels(ctx, tx, "user_models", "user_id", id, access); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, id)
}

// SetPassword replaces a user's password hash and ends all their sessions.
func (s *Store) SetPassword(ctx context.Context, id, hash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", hash, id)
	if err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id); err != nil {
		return fmt.Errorf("store: end sessions: %w", err)
	}
	return tx.Commit()
}

// DeleteUser removes a user with their sessions and keys.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountOwners returns how many enabled owners exist.
func (s *Store) CountOwners(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE role = ? AND disabled = 0", Owner).Scan(&n)
	return n, err
}

// Session tokens are stored hashed; a leaked database can't sign anyone in.

// CreateSession records a session and returns its id. A pending session
// is a sign-in that still needs its second factor.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, in Session) (Session, error) {
	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	in.ID, in.Created, in.LastSeen = id, now, now
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (id, token_hash, user_id, agent_hash, device, ip, pending, create_time, last_seen, expire_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tokenHash, in.UserID, agentHash(in.Device), in.Device, in.IP, in.Pending, millis(now), millis(now), millis(in.Expires))
	if err != nil {
		return Session{}, fmt.Errorf("store: create session: %w", err)
	}
	if !in.Pending {
		_, err = s.db.ExecContext(ctx, "UPDATE users SET last_sign_in = ? WHERE id = ?", millis(now), in.UserID)
	}
	return in, err
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

// LookupSession returns the session a live token belongs to and its user,
// bumping last_seen. device must match the one that created it.
func (s *Store) LookupSession(ctx context.Context, tokenHash, device string) (Session, User, error) {
	var se Session
	var hash string
	var created, seen, expire int64
	err := s.db.QueryRowContext(ctx, "SELECT id, user_id, agent_hash, device, ip, pending, create_time, last_seen, expire_time FROM sessions WHERE token_hash = ?", tokenHash).
		Scan(&se.ID, &se.UserID, &hash, &se.Device, &se.IP, &se.Pending, &created, &seen, &expire)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("store: session: %w", err)
	}
	now := time.Now()
	se.Created, se.LastSeen, se.Expires = time.UnixMilli(created).UTC(), time.UnixMilli(seen).UTC(), time.UnixMilli(expire).UTC()
	if !now.Before(se.Expires) || (!se.Pending && now.Sub(se.LastSeen) > SessionIdleTTL) {
		s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", se.ID)
		return Session{}, User{}, ErrNotFound
	}
	if hash != agentHash(device) {
		return Session{}, User{}, ErrWrongDevice
	}
	// Write last_seen at most once a minute; it's a hint, not a ledger.
	if now.Sub(se.LastSeen) > time.Minute {
		se.LastSeen = now.UTC()
		s.db.ExecContext(ctx, "UPDATE sessions SET last_seen = ? WHERE id = ?", millis(now), se.ID)
	}
	u, err := s.GetUser(ctx, se.UserID)
	return se, u, err
}

// ListSessions returns a user's live sessions, newest first.
func (s *Store) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, device, ip, create_time, last_seen, expire_time FROM sessions
		WHERE user_id = ? AND pending = 0 AND expire_time > ? AND last_seen > ? ORDER BY last_seen DESC`,
		userID, millis(time.Now()), millis(time.Now().Add(-SessionIdleTTL)))
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var se Session
		var created, seen, expire int64
		if err := rows.Scan(&se.ID, &se.UserID, &se.Device, &se.IP, &created, &seen, &expire); err != nil {
			return nil, err
		}
		se.Created, se.LastSeen, se.Expires = time.UnixMilli(created).UTC(), time.UnixMilli(seen).UTC(), time.UnixMilli(expire).UTC()
		out = append(out, se)
	}
	return out, rows.Err()
}

// DeleteSession ends one session by its token.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// RevokeSession ends one of a user's sessions by id.
func (s *Store) RevokeSession(ctx context.Context, userID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id = ?", userID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RevokeOtherSessions ends every session of a user except keep.
func (s *Store) RevokeOtherSessions(ctx context.Context, userID, keep string) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id <> ?", userID, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// EndSessions signs a user out everywhere.
func (s *Store) EndSessions(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
	return err
}

// ExpireSessions drops sessions past their end or idle for too long.
func (s *Store) ExpireSessions(ctx context.Context) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expire_time <= ? OR (pending = 0 AND last_seen <= ?)",
		millis(now), millis(now.Add(-SessionIdleTTL)))
	return err
}

// Two-step verification

// SetPendingTOTP stores a secret the user has been shown but not confirmed.
func (s *Store) SetPendingTOTP(ctx context.Context, userID, secret string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE users SET totp_pending = ? WHERE id = ?", secret, userID)
	return err
}

// EnableTOTP promotes the pending secret, stores hashed recovery codes and
// ends every other session, so a hijacked one can't ride along.
func (s *Store) EnableTOTP(ctx context.Context, userID, keepSession string, codeHashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE users SET totp_secret = totp_pending, totp_pending = '', totp_last_step = 0 WHERE id = ? AND totp_pending <> ''", userID); err != nil {
		return fmt.Errorf("store: enable totp: %w", err)
	}
	if err := replaceRecoveryCodes(ctx, tx, userID, codeHashes); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id <> ?", userID, keepSession); err != nil {
		return err
	}
	return tx.Commit()
}

// DisableTOTP turns two-step verification off and drops recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, userID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE users SET totp_secret = '', totp_pending = '', totp_last_step = 0 WHERE id = ?", userID); err != nil {
		return fmt.Errorf("store: disable totp: %w", err)
	}
	if err := replaceRecoveryCodes(ctx, tx, userID, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceRecoveryCodes(ctx context.Context, tx *sql.Tx, userID string, hashes []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM recovery_codes WHERE user_id = ?", userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, "INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)", userID, h); err != nil {
			return fmt.Errorf("store: recovery code: %w", err)
		}
	}
	return nil
}

// UseTOTPStep records that a code for step was accepted, refusing a replay.
// It reports whether the step was new.
func (s *Store) UseTOTPStep(ctx context.Context, userID string, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?", step, userID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UseRecoveryCode spends a recovery code and returns how many remain.
func (s *Store) UseRecoveryCode(ctx context.Context, userID, codeHash string) (bool, int, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM recovery_codes WHERE user_id = ? AND code_hash = ?", userID, codeHash)
	if err != nil {
		return false, 0, err
	}
	n, _ := res.RowsAffected()
	var left int
	err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM recovery_codes WHERE user_id = ?", userID).Scan(&left)
	return n > 0, left, err
}

const keyColumns = "id, user_id, name, prefix, role, all_models, create_time, last_used"

func scanKey(row scanner) (APIKey, error) {
	var k APIKey
	var created int64
	var used sql.NullInt64
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.Access.Role, &k.Access.AllModels, &created, &used)
	k.Created = time.UnixMilli(created).UTC()
	k.LastUsed = fromMillis(used)
	return k, err
}

// CreateAPIKey stores a key whose secret hashes to secretHash.
func (s *Store) CreateAPIKey(ctx context.Context, k APIKey, secretHash string) (APIKey, error) {
	id, err := newID()
	if err != nil {
		return APIKey{}, err
	}
	k.ID, k.Created = id, time.Now().UTC().Truncate(time.Millisecond)
	k.Access = normalize(k.Access)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO api_keys (id, user_id, name, prefix, secret_hash, role, all_models, create_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		k.ID, k.UserID, k.Name, k.Prefix, secretHash, k.Access.Role, k.Access.AllModels, millis(k.Created))
	if err != nil {
		return APIKey{}, fmt.Errorf("store: insert key: %w", err)
	}
	if err := setModels(ctx, tx, "api_key_models", "key_id", k.ID, k.Access); err != nil {
		return APIKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return APIKey{}, err
	}
	return k, nil
}

// ListAPIKeys returns a user's keys, newest first.
func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+keyColumns+" FROM api_keys WHERE user_id = ? ORDER BY create_time DESC, id DESC LIMIT 1000", userID)
	if err != nil {
		return nil, fmt.Errorf("store: list keys: %w", err)
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list keys: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Access.ModelIDs, err = s.models(ctx, "api_key_models", "key_id", out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// KeyUser returns the key with secretHash and its owner, and notes the use.
func (s *Store) KeyUser(ctx context.Context, secretHash string) (APIKey, User, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx, "SELECT "+keyColumns+" FROM api_keys WHERE secret_hash = ?", secretHash))
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, User{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, User{}, fmt.Errorf("store: key: %w", err)
	}
	if k.Access.ModelIDs, err = s.models(ctx, "api_key_models", "key_id", k.ID); err != nil {
		return APIKey{}, User{}, err
	}
	u, err := s.GetUser(ctx, k.UserID)
	if err != nil {
		return APIKey{}, User{}, err
	}
	// Best effort; a failed timestamp shouldn't fail the request.
	s.db.ExecContext(ctx, "UPDATE api_keys SET last_used = ? WHERE id = ?", millis(time.Now()), k.ID)
	return k, u, nil
}

// DeleteAPIKey removes one of userID's keys.
func (s *Store) DeleteAPIKey(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("store: delete key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
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
	_, err = s.db.ExecContext(ctx, "INSERT INTO audit_events (id, time, actor, action, target, detail, team_id) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''))",
		id, millis(e.Time), e.Actor, e.Action, e.Target, e.Detail, e.TeamID)
	if err != nil {
		return fmt.Errorf("store: audit: %w", err)
	}
	return nil
}

// ListAuditEvents returns up to limit of a team's events (every team's
// when team is "") older than the one with beforeID (every event when
// empty), newest first.
func (s *Store) ListAuditEvents(ctx context.Context, team, beforeID string, limit int) ([]AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.time, e.actor, e.action, e.target, e.detail, COALESCE(e.team_id, ''), COALESCE(t.name, '')
		FROM audit_events e LEFT JOIN teams t ON t.id = e.team_id
		WHERE (? = '' OR e.team_id = ?) AND (? = '' OR e.id < ?) ORDER BY e.id DESC LIMIT ?`,
		team, team, beforeID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.TeamID, &e.TeamName); err != nil {
			return nil, fmt.Errorf("store: list audit: %w", err)
		}
		e.Time = time.UnixMilli(t).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
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

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func setModels(ctx context.Context, tx execer, table, col, id string, a Access) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+col+" = ?", id); err != nil {
		return fmt.Errorf("store: clear %s: %w", table, err)
	}
	for _, m := range a.ModelIDs {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO "+table+" ("+col+", model_id) VALUES (?, ?)", id, m); err != nil {
			return fmt.Errorf("store: grant model: %w", err)
		}
	}
	return nil
}

func (s *Store) models(ctx context.Context, table, col, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT model_id FROM "+table+" WHERE "+col+" = ? ORDER BY model_id", id)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
