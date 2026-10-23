package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/gsoultan/storm"

	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/store/db/dataset"
	"github.com/gsoultan/mimir-server/internal/store/db/setting"
	"github.com/gsoultan/mimir-server/internal/store/db/user"
	"github.com/gsoultan/mimir-server/internal/store/db/webhook"
)

// Secrets at rest. Webhook signing secrets, dataset connection strings,
// alert settings (mail passwords, bot tokens) and two-step verification
// keys are sealed with AES-256-GCM
// as they are written and opened as they are read, here in the mapping
// between rows and the store's types, so the columns stay text and the
// rest of the server sees plaintext. Each value is bound to its table,
// column and row, so a sealed value copied to another row does not open.
//
// Rows written before encryption hold plaintext; reads pass it through,
// and Open seals every such row in place (see sealLegacy).

// Option changes how Open sets up the store.
type Option func(*options)

type options struct {
	key    []byte
	logURL string
}

// WithSecretKey sets the 32-byte key secrets are sealed with. Without it,
// Open uses the key file in the data folder, creating it on first start.
func WithSecretKey(key []byte) Option {
	return func(o *options) { o.key = key }
}

// WithLogDatabase keeps the audit log in a second PostgreSQL database at
// databaseURL instead of the main one. Open brings its schema up the same
// way. Events already in the main database stay there.
func WithLogDatabase(databaseURL string) Option {
	return func(o *options) { o.logURL = strings.TrimSpace(databaseURL) }
}

// secretSetting reports whether a setting holds credentials: the alert
// channels' settings carry a mail password and a bot token, and the
// cloud settings an API token and a worker's token and key.
func secretSetting(key string) bool {
	return strings.HasPrefix(key, "alerts.") || strings.HasPrefix(key, "cloud.")
}

func webhookContext(id string) string  { return "webhooks.secret:" + id }
func datasetContext(id string) string  { return "datasets.dsn:" + id }
func settingContext(key string) string { return "settings.value:" + key }

// totpContext binds a user's authenticator key. The confirmed and the
// pending column share it, so enabling can move the sealed value as is.
func totpContext(userID string) string { return "users.totp:" + userID }

// secretRow is one stored value of a sealed column and its row's key.
type secretRow struct{ key, value string }

// sealedColumn is one column the store seals: how to read its rows that
// hold a value, how to swap one row's plaintext for its sealed form, and
// each row's binding context.
type sealedColumn struct {
	name    string // table.column, for errors
	context func(key string) string
	rows    func(ctx context.Context, ex storm.Executor) ([]secretRow, error)
	// replace writes sealed over the row's value while it still holds
	// plain, and reports whether it did.
	replace func(ctx context.Context, ex storm.Executor, key, plain, sealed string) (bool, error)
}

var sealedColumns = []sealedColumn{{
	name:    "webhooks.secret",
	context: webhookContext,
	rows: func(ctx context.Context, ex storm.Executor) ([]secretRow, error) {
		rs, err := webhook.New().Where(webhook.Secret.NotEq("")).All(ctx, ex, nil)
		return collect(rs, func(r webhook.Row) secretRow { return secretRow{r.ID, r.Secret} }), err
	},
	replace: func(ctx context.Context, ex storm.Executor, key, plain, sealed string) (bool, error) {
		var m webhook.Mut
		m.SetSecret(sealed)
		n, err := webhook.New().Where(webhook.ID.Eq(key), webhook.Secret.Eq(plain)).UpdateAll(ctx, ex, m)
		return n > 0, err
	},
}, {
	name:    "datasets.dsn",
	context: datasetContext,
	rows: func(ctx context.Context, ex storm.Executor) ([]secretRow, error) {
		rs, err := dataset.New().Where(dataset.Dsn.NotEq("")).All(ctx, ex, nil)
		return collect(rs, func(r dataset.Row) secretRow { return secretRow{r.ID, r.Dsn} }), err
	},
	replace: func(ctx context.Context, ex storm.Executor, key, plain, sealed string) (bool, error) {
		var m dataset.Mut
		m.SetDsn(sealed)
		n, err := dataset.New().Where(dataset.ID.Eq(key), dataset.Dsn.Eq(plain)).UpdateAll(ctx, ex, m)
		return n > 0, err
	},
}, {
	name:    "settings.value",
	context: settingContext,
	rows: func(ctx context.Context, ex storm.Executor) ([]secretRow, error) {
		rs, err := setting.New().Where(setting.Key.Like("alerts.%"), setting.Value.NotEq("")).All(ctx, ex, nil)
		return collect(rs, func(r setting.Row) secretRow { return secretRow{r.Key, r.Value} }), err
	},
	replace: func(ctx context.Context, ex storm.Executor, key, plain, sealed string) (bool, error) {
		var m setting.Mut
		m.SetValue(sealed)
		n, err := setting.New().Where(setting.Key.Eq(key), setting.Value.Eq(plain)).UpdateAll(ctx, ex, m)
		return n > 0, err
	},
}, {
	name:    "users.totp_secret",
	context: totpContext,
	rows: func(ctx context.Context, ex storm.Executor) ([]secretRow, error) {
		rs, err := user.New().Where(user.TotpSecret.NotEq("")).All(ctx, ex, nil)
		return collect(rs, func(r user.Row) secretRow { return secretRow{r.ID, r.TotpSecret} }), err
	},
	replace: func(ctx context.Context, ex storm.Executor, key, plain, sealed string) (bool, error) {
		var m user.Mut
		m.SetTotpSecret(sealed)
		n, err := user.New().Where(user.ID.Eq(key), user.TotpSecret.Eq(plain)).UpdateAll(ctx, ex, m)
		return n > 0, err
	},
}, {
	name:    "users.totp_pending",
	context: totpContext,
	rows: func(ctx context.Context, ex storm.Executor) ([]secretRow, error) {
		rs, err := user.New().Where(user.TotpPending.NotEq("")).All(ctx, ex, nil)
		return collect(rs, func(r user.Row) secretRow { return secretRow{r.ID, r.TotpPending} }), err
	},
	replace: func(ctx context.Context, ex storm.Executor, key, plain, sealed string) (bool, error) {
		var m user.Mut
		m.SetTotpPending(sealed)
		n, err := user.New().Where(user.ID.Eq(key), user.TotpPending.Eq(plain)).UpdateAll(ctx, ex, m)
		return n > 0, err
	},
}}

func collect[R any](rs []R, f func(R) secretRow) []secretRow {
	out := make([]secretRow, len(rs))
	for i, r := range rs {
		out[i] = f(r)
	}
	return out
}

// sealLegacy seals, in one transaction, every stored secret still in
// plaintext, and checks that every one already sealed opens with this
// key, so a server started with the wrong key stops here rather than
// failing each webhook and refresh later. It returns how many rows it
// sealed.
//
// Each row is replaced only while it still holds the plaintext read, so
// servers sealing at the same start seal each row once: the second
// waits on the first's row lock and then finds nothing to replace.
func (s *Store) sealLegacy(ctx context.Context) (int, error) {
	n := 0
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		n = 0
		for _, c := range sealedColumns {
			m, err := s.sealColumn(ctx, ex, c)
			if err != nil {
				return err
			}
			n += m
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) sealColumn(ctx context.Context, ex storm.Executor, c sealedColumn) (int, error) {
	rows, err := c.rows(ctx, ex)
	if err != nil {
		return 0, fmt.Errorf("store: seal %s: %w", c.name, err)
	}
	n := 0
	for _, r := range rows {
		if secret.Sealed(r.value) {
			if _, err := s.box.Open(r.value, c.context(r.key)); err != nil {
				return 0, fmt.Errorf("store: %s %s: %w", c.name[:strings.IndexByte(c.name, '.')], r.key, err)
			}
			continue
		}
		v, err := s.box.Seal(r.value, c.context(r.key))
		if err != nil {
			return 0, err
		}
		done, err := c.replace(ctx, ex, r.key, r.value, v)
		if err != nil {
			return 0, fmt.Errorf("store: seal %s %s: %w", c.name, r.key, err)
		}
		if done {
			n++
		}
	}
	return n, nil
}

// Sealed reports how many plaintext secrets Open sealed in place.
func (s *Store) Sealed() int { return s.sealed }

// openSecret opens a stored value, naming what failed.
func (s *Store) openSecret(v, context string) (string, error) {
	p, err := s.box.Open(v, context)
	if err != nil {
		return "", fmt.Errorf("store: %s: %w", context[:strings.IndexByte(context, ':')], err)
	}
	return p, nil
}
