package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gsoultan/mimir-server/internal/secret"
)

// Secrets at rest. Webhook signing secrets, dataset connection strings and
// alert settings (mail passwords, bot tokens) are sealed with AES-256-GCM
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
	key []byte
}

// WithSecretKey sets the 32-byte key secrets are sealed with. Without it,
// Open uses the key file in the data folder, creating it on first start.
func WithSecretKey(key []byte) Option {
	return func(o *options) { o.key = key }
}

// secretSetting reports whether a setting holds credentials: the alert
// channels' settings carry a mail password and a bot token.
func secretSetting(key string) bool { return strings.HasPrefix(key, "alerts.") }

func webhookContext(id string) string  { return "webhooks.secret:" + id }
func datasetContext(id string) string  { return "datasets.dsn:" + id }
func settingContext(key string) string { return "settings.value:" + key }

// sealed is one column the store seals: its table, key column, value
// column, the rows it covers, and each row's binding context.
type sealed struct {
	table, key, value string
	where             string
	context           func(key string) string
}

var sealedColumns = []sealed{
	{"webhooks", "id", "secret", "", webhookContext},
	{"datasets", "id", "dsn", "", datasetContext},
	{"settings", "key", "value", ` AND "key" LIKE 'alerts.%'`, settingContext},
}

// sealLock serialises servers sealing legacy rows at the same start.
const sealLock = 0x6d696d6972_5345 // "mimir", "SE"

// sealLegacy seals, in one transaction, every stored secret still in
// plaintext, and checks that every one already sealed opens with this
// key, so a server started with the wrong key stops here rather than
// failing each webhook and refresh later. It returns how many rows it
// sealed.
func (s *Store) sealLegacy(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: seal secrets: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(sealLock)); err != nil {
		return 0, fmt.Errorf("store: seal secrets: %w", err)
	}
	n := 0
	for _, c := range sealedColumns {
		m, err := s.sealColumn(ctx, tx, c)
		if err != nil {
			return n, err
		}
		n += m
	}
	if err := tx.Commit(ctx); err != nil {
		return n, fmt.Errorf("store: seal secrets: %w", err)
	}
	return n, nil
}

func (s *Store) sealColumn(ctx context.Context, tx pgx.Tx, c sealed) (int, error) {
	key, value, table := pgx.Identifier{c.key}.Sanitize(), pgx.Identifier{c.value}.Sanitize(), pgx.Identifier{c.table}.Sanitize()
	rows, err := tx.Query(ctx, `SELECT `+key+`, `+value+` FROM `+table+` WHERE `+value+` <> ''`+c.where)
	if err != nil {
		return 0, fmt.Errorf("store: seal %s.%s: %w", c.table, c.value, err)
	}
	type row struct{ k, v string }
	var plain []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.k, &r.v); err != nil {
			rows.Close()
			return 0, fmt.Errorf("store: seal %s.%s: %w", c.table, c.value, err)
		}
		if !secret.Sealed(r.v) {
			plain = append(plain, r)
			continue
		}
		if _, err := s.box.Open(r.v, c.context(r.k)); err != nil {
			rows.Close()
			return 0, fmt.Errorf("store: %s %s: %w", c.table, r.k, err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: seal %s.%s: %w", c.table, c.value, err)
	}
	for _, r := range plain {
		v, err := s.box.Seal(r.v, c.context(r.k))
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE `+table+` SET `+value+` = $1 WHERE `+key+` = $2 AND `+value+` = $3`, v, r.k, r.v); err != nil {
			return 0, fmt.Errorf("store: seal %s %s: %w", c.table, r.k, err)
		}
	}
	return len(plain), nil
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
