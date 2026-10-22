// Package setup is first-run setup for a console started with no database:
// it takes the main database, an optional log database, the secret key,
// how long history is kept and the first owner, applies them and writes
// the configuration file, after which the full server starts.
//
// Until the file exists anyone who reaches the port could claim the server,
// so every step that changes anything needs the one-time setup token the
// server prints in its log (or $MIMIR_SETUP_TOKEN), guesses are limited per
// address, and once setup is done it is closed for good: the next start
// finds the file and never offers it again, and the owner Setup made shuts
// the account step too.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/config"
	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/store"
)

// KeySource is where the secret key comes from.
type KeySource int

const (
	// KeyNone means there is no key yet: setup makes or takes one and saves
	// it in the data folder.
	KeyNone KeySource = iota
	// KeyEnvironment means $MIMIR_SECRET_KEY or -secret-key-file.
	KeyEnvironment
	// KeyFile means the data folder's key file, already there.
	KeyFile
)

// Key is the secret key as setup finds it before it runs.
type Key struct {
	Source KeySource
	// Value is the key unless Source is KeyNone.
	Value []byte
	// File is where a KeyNone key is saved, or where a KeyFile key is.
	File string
}

// FindKey looks for the key the way the server does at startup, without
// making one: envValue is $MIMIR_SECRET_KEY, file -secret-key-file.
func FindKey(envValue, file, dataDir string) (Key, error) {
	if strings.TrimSpace(envValue) != "" || file != "" {
		k, _, err := secret.Load(envValue, file, dataDir)
		if err != nil {
			return Key{}, err
		}
		return Key{Source: KeyEnvironment, Value: k, File: file}, nil
	}
	path := filepath.Join(dataDir, secret.DefaultFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Key{Source: KeyNone, File: path}, nil
	}
	k, _, err := secret.Load("", path, dataDir)
	if err != nil {
		return Key{}, err
	}
	return Key{Source: KeyFile, Value: k, File: path}, nil
}

// Owner is the first owner to create.
type Owner struct {
	Email, Name string
	// PasswordHash is from auth.HashPassword.
	PasswordHash string
}

// Plan is everything setup applies.
type Plan struct {
	DataDir    string
	ConfigPath string
	// DatabaseURL is required; LogDatabaseURL may be empty. Either may be an
	// env: or file: reference (see package config).
	DatabaseURL, LogDatabaseURL string
	Key                         Key
	// NewKey is the key to save when Key.Source is KeyNone: one the person
	// gave, or nil to make a new one.
	NewKey []byte
	// Retention is nil to keep the defaults.
	Retention *api.Retention
	// Owner is created when the database has no accounts yet; nil makes
	// none.
	Owner *Owner
}

// Result says what Apply did.
type Result struct {
	// Config is the file Apply wrote, with its URLs as given (not sealed).
	Config config.File
	// KeyGenerated is true when Apply made a new key, KeySaved when it
	// saved one to Key.File.
	KeyGenerated, KeySaved bool
	// OwnerCreated is false when the database already had accounts.
	OwnerCreated bool
	// MigrationSteps is how many schema steps the databases took.
	MigrationSteps int
}

// CheckURL checks a database URL is a PostgreSQL one, with an error fit to
// show.
func CheckURL(raw, what string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" && u.Query().Get("host") == "" {
		return fmt.Errorf("The %s must be a PostgreSQL URL, such as postgres://mimir:secret@localhost:5432/mimir.", what)
	}
	return nil
}

// Apply runs setup: it brings the databases' schema up (refusing a key
// that doesn't open secrets already sealed there), saves the retention and
// creates the owner, saves the key and finally writes the configuration
// file, last so a failure part way leaves setup open to try again. It
// never replaces a key file or a configuration file already there.
func Apply(ctx context.Context, p Plan) (Result, error) {
	var res Result
	// The URLs may be env: and file: references, kept as they are in the
	// file and followed to connect.
	dbURL, err := config.Resolve(nil, p.DatabaseURL, "database_url")
	if err != nil {
		return res, err
	}
	if err := CheckURL(dbURL, "database URL"); err != nil {
		return res, err
	}
	logURL, err := config.Resolve(nil, p.LogDatabaseURL, "log_database_url")
	if err != nil {
		return res, err
	}
	if logURL != "" {
		if err := CheckURL(logURL, "log database URL"); err != nil {
			return res, err
		}
	}
	if _, err := os.Stat(p.ConfigPath); err == nil {
		return res, config.ErrExists
	}
	// The data folder will hold the key and the file; keep it to the owner
	// when it is new.
	if err := os.MkdirAll(p.DataDir, 0o700); err != nil {
		return res, err
	}
	key := p.Key.Value
	if p.Key.Source == KeyNone {
		key = p.NewKey
		if key == nil {
			k, err := secret.Generate()
			if err != nil {
				return res, err
			}
			key, res.KeyGenerated = k, true
		}
	}
	box, err := secret.New(key)
	if err != nil {
		return res, err
	}

	opts := []store.Option{store.WithSecretKey(key)}
	if logURL != "" && logURL != dbURL {
		opts = append(opts, store.WithLogDatabase(logURL))
	}
	st, err := store.Open(ctx, dbURL, p.DataDir, opts...)
	if err != nil {
		return res, err
	}
	defer st.Close()
	res.MigrationSteps = st.Applied()
	if p.Retention != nil {
		if err := api.SaveRetention(ctx, st, *p.Retention); err != nil {
			return res, err
		}
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return res, err
	}
	if n == 0 && p.Owner != nil {
		u, err := st.CreateUser(ctx, store.User{Email: p.Owner.Email, Name: p.Owner.Name, PasswordHash: p.Owner.PasswordHash, Access: store.Access{Role: store.Owner, AllModels: true}})
		if err != nil {
			return res, err
		}
		res.OwnerCreated = true
		st.Audit(ctx, store.AuditEvent{Actor: u.Email, Action: "server.setup", Target: u.Email, TeamID: u.TeamID})
	}

	// The key is saved before the file that depends on it, and only once
	// the database took it.
	if p.Key.Source == KeyNone {
		if err := secret.Save(p.Key.File, key); err != nil {
			return res, err
		}
		res.KeySaved = true
	}
	res.Config = config.File{DatabaseURL: p.DatabaseURL, LogDatabaseURL: p.LogDatabaseURL}
	if p.Key.Source == KeyEnvironment && p.Key.File != "" {
		res.Config.SecretKeyFile = p.Key.File
	}
	sealed, err := res.Config.Seal(box)
	if err != nil {
		return res, err
	}
	if err := config.Write(p.ConfigPath, sealed); err != nil {
		return res, err
	}
	return res, nil
}

// checkTimeout bounds a connection check, so a URL pointing at a host that
// never answers doesn't hold the request.
const checkTimeout = 10 * time.Second

// Scrub removes the password in databaseURL from msg, for errors shown or
// logged.
func Scrub(msg, databaseURL string) string {
	u, err := url.Parse(databaseURL)
	if err != nil || u.User == nil {
		return msg
	}
	if pw, ok := u.User.Password(); ok && pw != "" {
		msg = strings.ReplaceAll(msg, pw, "xxxxx")
		msg = strings.ReplaceAll(msg, url.QueryEscape(pw), "xxxxx")
		msg = strings.ReplaceAll(msg, url.PathEscape(pw), "xxxxx")
	}
	return msg
}
