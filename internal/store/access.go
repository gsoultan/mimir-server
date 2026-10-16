package store

import (
	"context"
	"database/sql"
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

const userColumns = "id, email, name, password_hash, role, all_models, disabled, create_time, last_sign_in"

func scanUser(row scanner) (User, error) {
	var u User
	var created int64
	var signIn sql.NullInt64
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Access.Role, &u.Access.AllModels, &u.Disabled, &created, &signIn)
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO users ("+userColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)",
		u.ID, u.Email, u.Name, u.PasswordHash, u.Access.Role, u.Access.AllModels, u.Disabled, millis(u.Created))
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

// ListUsers returns every user, oldest first.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY create_time, id LIMIT 1000")
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

// CreateSession records a session for user until expire.
func (s *Store) CreateSession(ctx context.Context, tokenHash, userID string, expire time.Time) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, "INSERT INTO sessions (token_hash, user_id, create_time, expire_time) VALUES (?, ?, ?, ?)",
		tokenHash, userID, millis(now), millis(expire))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	_, err = s.db.ExecContext(ctx, "UPDATE users SET last_sign_in = ? WHERE id = ?", millis(now), userID)
	return err
}

// SessionUser returns the user a live session token belongs to.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (User, error) {
	var userID string
	err := s.db.QueryRowContext(ctx, "SELECT user_id FROM sessions WHERE token_hash = ? AND expire_time > ?",
		tokenHash, millis(time.Now())).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: session: %w", err)
	}
	return s.GetUser(ctx, userID)
}

// DeleteSession ends one session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// ExpireSessions drops sessions past their end.
func (s *Store) ExpireSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expire_time <= ?", millis(time.Now()))
	return err
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
	_, err = s.db.ExecContext(ctx, "INSERT INTO audit_events (id, time, actor, action, target, detail) VALUES (?, ?, ?, ?, ?, ?)",
		id, millis(e.Time), e.Actor, e.Action, e.Target, e.Detail)
	if err != nil {
		return fmt.Errorf("store: audit: %w", err)
	}
	return nil
}

// ListAuditEvents returns up to limit events older than the one with
// beforeID (every event when empty), newest first.
func (s *Store) ListAuditEvents(ctx context.Context, beforeID string, limit int) ([]AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, time, actor, action, target, detail FROM audit_events WHERE (? = '' OR id < ?) ORDER BY id DESC LIMIT ?",
		beforeID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Actor, &e.Action, &e.Target, &e.Detail); err != nil {
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
