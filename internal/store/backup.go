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
)

// ErrBackupUnavailable means the server can't make a backup itself because
// pg_dump isn't installed where it runs.
var ErrBackupUnavailable = errors.New("store: pg_dump is not installed on this server; back up the database with pg_dump --format=custom")

// Backup writes a consistent copy of the database to path, in pg_dump's
// custom format (restore it with pg_restore). It runs pg_dump from PATH,
// without a shell; the password reaches it through PGPASSWORD rather than
// the command line, where other users of the machine could read it.
func (s *Store) Backup(ctx context.Context, path string) error {
	bin, err := exec.LookPath("pg_dump")
	if err != nil {
		return ErrBackupUnavailable
	}
	conn, password, err := libpqURL(s.url)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "--format=custom", "--no-owner", "--file="+path, "--dbname="+conn)
	cmd.Env = os.Environ()
	if password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("store: pg_dump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
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
