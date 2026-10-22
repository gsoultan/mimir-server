package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/migrate"
	schemapg "github.com/gsoultan/storm/schema/pg"

	"github.com/gsoultan/mimir-server/internal/store/tables"
)

// ErrBackupUnavailable means the server can't make a backup itself because
// pg_dump isn't installed where it runs.
var ErrBackupUnavailable = errors.New("store: pg_dump is not installed on this server; back up the database with pg_dump --format=custom")

// ErrRestoreUnavailable means pg_restore isn't installed where the restore
// runs.
var ErrRestoreUnavailable = errors.New("store: pg_restore is not installed here; install the PostgreSQL client tools (the same major version as the database or newer)")

// Backup writes a consistent copy of the database to path, in pg_dump's
// custom format (restore it with pg_restore). It runs pg_dump from PATH,
// without a shell; the password reaches it through PGPASSWORD rather than
// the command line, where other users of the machine could read it.
func (s *Store) Backup(ctx context.Context, path string) error {
	return Dump(ctx, s.url, path)
}

// Dump is Backup for the database at databaseURL, which it does not open
// or change: `mimir-server backup` dumps the main and log databases with
// it without bringing their schemas up to date first.
func Dump(ctx context.Context, databaseURL, path string) error {
	bin, err := exec.LookPath("pg_dump")
	if err != nil {
		return ErrBackupUnavailable
	}
	if err := pgTool(ctx, bin, databaseURL, "--format=custom", "--no-owner", "--file="+path); err != nil {
		return err
	}
	// pg_dump makes the file by the umask; it holds every account's
	// password hash, so it is the owner's alone.
	return os.Chmod(path, 0o600)
}

// Restore loads a dump Dump wrote into the database at databaseURL with
// pg_restore, in one transaction, so a restore that fails half way leaves
// the database as it was. With replace, the objects the dump holds are
// dropped first; without it the database should be empty (see Empty).
// Tables the dump doesn't hold stay, so roll back from a newer version
// into an empty database instead. Ownership and grants are left out, so
// the restored tables belong to the role in databaseURL, as the server
// expects.
func Restore(ctx context.Context, databaseURL, path string, replace bool) error {
	bin, err := exec.LookPath("pg_restore")
	if err != nil {
		return ErrRestoreUnavailable
	}
	args := []string{"--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction"}
	if replace {
		args = append(args, "--clean", "--if-exists")
	}
	return pgTool(ctx, bin, databaseURL, append(args, path)...)
}

// pgTool runs one of PostgreSQL's client programs against databaseURL.
func pgTool(ctx context.Context, bin, databaseURL string, args ...string) error {
	conn, password, err := libpqURL(databaseURL)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"--dbname=" + conn}, args...)...)
	cmd.Env = os.Environ()
	if password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		name := bin[strings.LastIndexAny(bin, `/\`)+1:]
		return fmt.Errorf("store: %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Empty reports whether the database at databaseURL has no tables yet, so
// a restore can go into it without replacing anything.
func Empty(ctx context.Context, databaseURL string) (bool, error) {
	pool, err := connect(ctx, databaseURL)
	if err != nil {
		return false, err
	}
	defer pool.Close()
	cur, err := schemapg.Introspect(ctx, pool, "public")
	if err != nil {
		return false, fmt.Errorf("store: read the schema: %w", err)
	}
	return len(cur.Tables) == 0, nil
}

// Step is one schema change an upgrade would make.
type Step struct {
	SQL string
	// Refused is a step that could lose data, which the server will not
	// apply on its own; Why says what it would lose.
	Refused bool
	Why     string
}

// Plan lists the schema changes the server would make when it next starts
// on the database at databaseURL, without making them. With log it plans
// for a separate audit log database rather than the main one. Planning
// creates and drops a scratch schema, so the role needs CREATE, as it does
// to migrate.
func Plan(ctx context.Context, databaseURL string, log bool) ([]Step, error) {
	pool, err := connect(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	model := tables.All()
	if log {
		model = tables.LogAll()
	}
	want, err := storm.Build(model...)
	if err != nil {
		return nil, fmt.Errorf("store: model: %w", err)
	}
	c, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	defer c.Release()
	plan, err := migrate.For(ctx, c.Conn(), "public", want)
	if err != nil {
		return nil, fmt.Errorf("store: plan: %w", err)
	}
	steps := make([]Step, 0, len(plan.Changes))
	for _, ch := range plan.Changes {
		steps = append(steps, Step{SQL: ch.SQL, Refused: ch.Destructive && !acceptReplaced(ch), Why: ch.Why})
	}
	return steps, nil
}

// libpqURL turns the server's database URL into one pg_dump accepts: the
// password comes out (it is returned for PGPASSWORD), and so do the pool
// options only pgx understands. A keyword=value string is passed through.
func libpqURL(databaseURL string) (conn, password string, err error) {
	if !strings.HasPrefix(databaseURL, "postgres://") && !strings.HasPrefix(databaseURL, "postgresql://") {
		return databaseURL, "", nil
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", "", fmt.Errorf("store: database URL: %w", err)
	}
	if u.User != nil {
		password, _ = u.User.Password()
		u.User = url.User(u.User.Username())
	}
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(k, "pool_") {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), password, nil
}
