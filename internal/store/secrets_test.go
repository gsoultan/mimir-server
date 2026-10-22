package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/testdb"
)

func key(b byte) []byte {
	k := make([]byte, secret.KeySize)
	for i := range k {
		k[i] = b
	}
	return k
}

// raw reads one stored value as it sits in the database.
func raw(t *testing.T, url, query string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var v string
	if err := conn.QueryRow(ctx, query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

// TestSecretsSealedAtRest writes each kind of secret through the store and
// checks the database holds only ciphertext while the store reads back the
// plaintext.
func TestSecretsSealedAtRest(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	s, err := Open(ctx, url, t.TempDir(), WithSecretKey(key(1)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	w, err := s.CreateWebhook(ctx, Webhook{Name: "hook", URL: "https://example.com/h", Secret: "whsec", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDataset(ctx, Dataset{Name: "q", Kind: DatasetTable, Driver: "postgres", DSN: "postgres://u:pw@db/x", Query: "SELECT 1"}, []byte("a\n1\n"))
	if err != nil {
		t.Fatal(err)
	}
	const alert = `{"enabled":true,"bot_token":"123:abc","chat_id":"9"}`
	if err := s.SetSetting(ctx, "alerts.telegram", alert); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "retention", `{"audit_days":30}`); err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, User{Email: "a@example.com", Name: "A", PasswordHash: "x", Access: Access{Role: Viewer}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPendingTOTP(ctx, u.ID, "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	if v := raw(t, url, `SELECT totp_pending FROM users WHERE id = $1`, u.ID); !secret.Sealed(v) {
		t.Errorf("pending authenticator key stored as %q, want it sealed", v)
	}
	if got, err := s.GetUser(ctx, u.ID); err != nil || got.TOTPPending != "JBSWY3DPEHPK3PXP" {
		t.Errorf("GetUser pending = %+v, %v", got, err)
	}
	if err := s.EnableTOTP(ctx, u.ID, "", nil); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ query, id, plain string }{
		{`SELECT secret FROM webhooks WHERE id = $1`, w.ID, "whsec"},
		{`SELECT dsn FROM datasets WHERE id = $1`, d.ID, "postgres://u:pw@db/x"},
		{`SELECT value FROM settings WHERE key = $1`, "alerts.telegram", alert},
		{`SELECT totp_secret FROM users WHERE id = $1`, u.ID, "JBSWY3DPEHPK3PXP"},
	} {
		v := raw(t, url, c.query, c.id)
		if !secret.Sealed(v) || strings.Contains(v, c.plain) {
			t.Errorf("%s stored %q, want it sealed", c.query, v)
		}
	}
	if v := raw(t, url, `SELECT value FROM settings WHERE key = 'retention'`); v != `{"audit_days":30}` {
		t.Errorf("plain setting stored as %q", v)
	}

	if got, err := s.GetWebhook(ctx, w.ID); err != nil || got.Secret != "whsec" {
		t.Errorf("GetWebhook = %+v, %v", got, err)
	}
	if list, err := s.ListWebhooks(ctx, DefaultTeam); err != nil || len(list) != 1 || list[0].Secret != "whsec" {
		t.Errorf("ListWebhooks = %+v, %v", list, err)
	}
	if got, err := s.GetDataset(ctx, d.ID); err != nil || got.DSN != "postgres://u:pw@db/x" {
		t.Errorf("GetDataset = %+v, %v", got, err)
	}
	if list, err := s.ListDatasets(ctx, DefaultTeam); err != nil || len(list) != 1 || list[0].DSN != "postgres://u:pw@db/x" {
		t.Errorf("ListDatasets = %+v, %v", list, err)
	}
	if got, err := s.GetSetting(ctx, "alerts.telegram"); err != nil || got != alert {
		t.Errorf("GetSetting = %q, %v", got, err)
	}
	if got, err := s.GetUserByEmail(ctx, "a@example.com"); err != nil || got.TOTPSecret != "JBSWY3DPEHPK3PXP" || got.TOTPPending != "" {
		t.Errorf("GetUserByEmail = %+v, %v", got, err)
	}
	if list, err := s.ListUsers(ctx, ""); err != nil || len(list) != 1 || list[0].TOTPSecret != "JBSWY3DPEHPK3PXP" {
		t.Errorf("ListUsers = %+v, %v", list, err)
	}
	if s.Sealed() != 0 {
		t.Errorf("fresh database: Sealed() = %d, want 0", s.Sealed())
	}
}

// TestLegacyPlaintextSecretsUpgrade starts on a database a server before
// encryption wrote: its secrets are plaintext. Open seals them in place,
// the store still reads them, and a later start with another key refuses
// to run rather than fail each webhook.
func TestLegacyPlaintextSecretsUpgrade(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	dir := t.TempDir()

	s, err := Open(ctx, url, dir, WithSecretKey(key(2)))
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, User{Email: "old@example.com", Name: "Old", PasswordHash: "x", Access: Access{Role: Viewer}})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	const (
		hookID = "01900000-0000-7000-8000-0000000000d1"
		dataID = "01900000-0000-7000-8000-0000000000b1"
		email  = `{"enabled":true,"host":"smtp.example.com","password":"mailpw"}`
	)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO webhooks (id, name, url, secret, create_time, team_id) VALUES ($1, 'old', 'https://example.com/o', 'plainsecret', now(), $2)`, []any{hookID, DefaultTeam}},
		{`INSERT INTO datasets (id, name, kind, rows, create_time, team_id, driver, dsn, source_query) VALUES ($1, 'old', 1, 1, now(), $2, 'postgres', 'postgres://u:plainpw@db/x', 'SELECT 1')`, []any{dataID, DefaultTeam}},
		{`INSERT INTO settings (key, value) VALUES ('alerts.email', $1)`, []any{email}},
		{`UPDATE users SET totp_secret = 'PLAINTOTPKEY2345', totp_pending = 'PLAINPENDING2345' WHERE id = $1`, []any{u.ID}},
	} {
		if _, err := conn.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q.sql)
		}
	}
	conn.Close(ctx)

	s, err = Open(ctx, url, dir, WithSecretKey(key(2)))
	if err != nil {
		t.Fatalf("Open on plaintext secrets: %v", err)
	}
	if s.Sealed() != 5 {
		t.Errorf("Sealed() = %d, want 5", s.Sealed())
	}
	for _, q := range []string{
		`SELECT secret FROM webhooks WHERE id = '` + hookID + `'`,
		`SELECT dsn FROM datasets WHERE id = '` + dataID + `'`,
		`SELECT value FROM settings WHERE key = 'alerts.email'`,
		`SELECT totp_secret FROM users WHERE id = '` + u.ID + `'`,
		`SELECT totp_pending FROM users WHERE id = '` + u.ID + `'`,
	} {
		if v := raw(t, url, q); !secret.Sealed(v) || strings.Contains(v, "plain") || strings.Contains(v, "PLAIN") || strings.Contains(v, "mailpw") {
			t.Errorf("%s = %q after upgrade, want sealed", q, v)
		}
	}
	if w, err := s.GetWebhook(ctx, hookID); err != nil || w.Secret != "plainsecret" {
		t.Errorf("GetWebhook = %+v, %v", w, err)
	}
	if d, err := s.GetDataset(ctx, dataID); err != nil || d.DSN != "postgres://u:plainpw@db/x" {
		t.Errorf("GetDataset = %+v, %v", d, err)
	}
	if v, err := s.GetSetting(ctx, "alerts.email"); err != nil || v != email {
		t.Errorf("GetSetting = %q, %v", v, err)
	}
	if got, err := s.GetUser(ctx, u.ID); err != nil || got.TOTPSecret != "PLAINTOTPKEY2345" || got.TOTPPending != "PLAINPENDING2345" {
		t.Errorf("GetUser = %+v, %v", got, err)
	}
	s.Close()

	// Nothing left to seal on the next start.
	s, err = Open(ctx, url, dir, WithSecretKey(key(2)))
	if err != nil {
		t.Fatal(err)
	}
	if s.Sealed() != 0 {
		t.Errorf("second Open sealed %d, want 0", s.Sealed())
	}
	s.Close()

	if _, err := Open(ctx, url, dir, WithSecretKey(key(3))); !errors.Is(err, secret.ErrWrongKey) {
		t.Errorf("Open with another key = %v, want ErrWrongKey", err)
	}
}

// TestSecretBoundToRow checks a sealed value moved to another row does not
// open there.
func TestSecretBoundToRow(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	s, err := Open(ctx, url, t.TempDir(), WithSecretKey(key(4)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.CreateWebhook(ctx, Webhook{Name: "a", URL: "https://example.com/a", Secret: "aaa"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateWebhook(ctx, Webhook{Name: "b", URL: "https://example.com/b", Secret: "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `UPDATE webhooks SET secret = (SELECT secret FROM webhooks WHERE id = $1) WHERE id = $2`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWebhook(ctx, b.ID); !errors.Is(err, secret.ErrWrongKey) {
		t.Errorf("GetWebhook with a copied secret = %v, want ErrWrongKey", err)
	}
}
