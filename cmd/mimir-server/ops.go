package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gsoultan/mimir-server/internal/backup"
	"github.com/gsoultan/mimir-server/internal/config"
	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/setup"
	"github.com/gsoultan/mimir-server/internal/store"
)

// opsFlags are the flags backup, restore and migrate share: where the
// console's configuration, databases and key are, given as the console is
// started.
type opsFlags struct {
	data, configFile, databaseURL, logDatabaseURL, secretKeyFile *string
}

func addOpsFlags(fl *flag.FlagSet) opsFlags {
	return opsFlags{
		data:           fl.String("data", "./data", "the data folder, as the console is started with"),
		configFile:     fl.String("config", os.Getenv(config.Env), "the configuration file ($MIMIR_CONFIG); default <data>/mimir.json"),
		databaseURL:    fl.String("database-url", os.Getenv("MIMIR_DATABASE_URL"), "the main database, when it isn't the configuration file's ($MIMIR_DATABASE_URL)"),
		logDatabaseURL: fl.String("log-database-url", os.Getenv("MIMIR_LOG_DATABASE_URL"), "the audit log database, when it isn't the configuration file's ($MIMIR_LOG_DATABASE_URL)"),
		secretKeyFile:  fl.String("secret-key-file", os.Getenv("MIMIR_SECRET_KEY_FILE"), "the secret key file, when it isn't the data folder's ($MIMIR_SECRET_KEY_FILE); $MIMIR_SECRET_KEY comes first"),
	}
}

// opsTarget is what the console would start with.
type opsTarget struct {
	cfg     config.File
	cfgPath string
	// key is nil when there is no key yet; keyFile is where it is kept,
	// "" when it comes from $MIMIR_SECRET_KEY.
	key     []byte
	keyFile string
}

// separateLog reports whether the audit log has a database of its own.
func (t opsTarget) separateLog() bool {
	return t.cfg.LogDatabaseURL != "" && t.cfg.LogDatabaseURL != t.cfg.DatabaseURL
}

// resolve finds the databases and key the way the console does at start:
// flags and the environment first, then the configuration file. Unlike the
// console it never makes a key.
func (o opsFlags) resolve() (opsTarget, error) {
	t := opsTarget{
		cfg:     config.File{DatabaseURL: *o.databaseURL, LogDatabaseURL: *o.logDatabaseURL, SecretKeyFile: *o.secretKeyFile},
		cfgPath: config.Path(*o.configFile, *o.data),
	}
	if t.cfg.DatabaseURL == "" {
		f, err := config.Read(t.cfgPath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return t, fmt.Errorf("no configuration at %s: pass -data, -config or -database-url as the console is started with", t.cfgPath)
		case err != nil:
			return t, err
		}
		t.cfg.DatabaseURL = f.DatabaseURL
		t.cfg.LogDatabaseURL = cmp.Or(t.cfg.LogDatabaseURL, f.LogDatabaseURL)
		t.cfg.SecretKeyFile = cmp.Or(t.cfg.SecretKeyFile, f.SecretKeyFile)
	}
	k, err := setup.FindKey(os.Getenv(secret.Env), t.cfg.SecretKeyFile, *o.data)
	if err != nil {
		return t, err
	}
	t.key, t.keyFile = k.Value, k.File
	var box *secret.Box
	if t.key != nil {
		if box, err = secret.New(t.key); err != nil {
			return t, err
		}
	}
	if t.cfg, err = t.cfg.Open(box); err != nil {
		return t, err
	}
	return t, nil
}

// opsContext is the context and log a command runs with.
func opsContext() (context.Context, context.CancelFunc, *slog.Logger) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx, stop, slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// parseOps parses a command's flags, reporting -h as done.
func parseOps(fl *flag.FlagSet, args []string) (help bool, err error) {
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, err
	}
	if fl.NArg() > 0 {
		return false, fmt.Errorf("unexpected argument %q", fl.Arg(0))
	}
	return false, nil
}

// runBackup is `mimir-server backup`: the main database, the log database
// when it has its own, and the data folder, into a new folder.
func runBackup(args []string) error {
	fl := flag.NewFlagSet("backup", flag.ContinueOnError)
	fl.Usage = func() {
		fmt.Fprintln(fl.Output(), "Usage: mimir-server backup -out FOLDER [flags]")
		fmt.Fprintln(fl.Output(), "\nBacks up the console's databases (with pg_dump, which must be on PATH) and its data folder into a new folder in FOLDER, named for the time, and prints its path. The secret key is left out: keep it apart from the backups. It can run while the console does.")
		fl.PrintDefaults()
	}
	o := addOpsFlags(fl)
	out := fl.String("out", "", "the folder to make the backup in")
	keep := fl.Int("keep", 0, "then remove all but this many of the newest backups in -out; 0 keeps them all")
	noFiles := fl.Bool("no-files", false, "leave the data folder out (back it up some other way)")
	if help, err := parseOps(fl, args); help || err != nil {
		return err
	}
	if *out == "" {
		return errors.New("pass -out, the folder to make the backup in")
	}
	if *keep < 0 {
		return errors.New("-keep can't be negative")
	}
	t, err := o.resolve()
	if err != nil {
		return err
	}
	ctx, stop, log := opsContext()
	defer stop()

	m := backup.Manifest{Format: backup.Format, Created: time.Now().UTC(), Version: version, Schema: store.LatestSchemaVersion(), LogDatabase: t.separateLog(), Files: !*noFiles}
	if t.key != nil {
		m.KeyID = secret.KeyID(t.key)
	} else {
		log.Warn("no secret key found; the backup is made without its id, so a restore can't check the key")
	}
	dir, err := backup.NewDir(*out, m.Created)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(dir)
		}
	}()
	// The databases first, then the files: a model file written after the
	// dump is harmless, one missing from the archive is not.
	if err := store.Dump(ctx, t.cfg.DatabaseURL, filepath.Join(dir, backup.MainDump)); err != nil {
		return err
	}
	log.Info("backup: main database", "database", store.Redact(t.cfg.DatabaseURL))
	if m.LogDatabase {
		if err := store.Dump(ctx, t.cfg.LogDatabaseURL, filepath.Join(dir, backup.LogDump)); err != nil {
			return fmt.Errorf("log database: %w", err)
		}
		log.Info("backup: log database", "database", store.Redact(t.cfg.LogDatabaseURL))
	}
	if m.Files {
		// Never the key, the configuration (it belongs to this machine) or
		// the backups themselves, should -out be in the data folder.
		skip := []string{filepath.Join(*o.data, secret.DefaultFile), t.keyFile, t.cfgPath, *out}
		n, err := backup.ArchiveFiles(*o.data, filepath.Join(dir, backup.FilesArchive), skip)
		if err != nil {
			return err
		}
		log.Info("backup: data folder", "data", *o.data, "files", n)
	}
	if err := backup.WriteManifest(dir, m); err != nil {
		return err
	}
	done = true
	removed, err := backup.Prune(*out, *keep)
	for _, r := range removed {
		log.Info("backup: removed an old backup", "path", r)
	}
	if err != nil {
		return err
	}
	fmt.Println(dir)
	return nil
}

// runRestore is `mimir-server restore`: a backup put back into the
// databases and data folder the console is configured with.
func runRestore(args []string) error {
	fl := flag.NewFlagSet("restore", flag.ContinueOnError)
	fl.Usage = func() {
		fmt.Fprintln(fl.Output(), "Usage: mimir-server restore -from BACKUP [flags]")
		fmt.Fprintln(fl.Output(), "\nPuts a backup from `mimir-server backup` back (with pg_restore, which must be on PATH) into the databases and data folder the console is configured with. Stop the console first. The databases must be empty unless -replace is given, and the secret key must be the one the backup was made with. The next start brings the schema up to date.")
		fl.PrintDefaults()
	}
	o := addOpsFlags(fl)
	from := fl.String("from", "", "the backup folder, as `mimir-server backup` printed it")
	replace := fl.Bool("replace", false, "restore over what the databases and data folder hold, for a backup of this version; to roll back from a newer one, restore into empty databases")
	noFiles := fl.Bool("no-files", false, "leave the data folder as it is")
	if help, err := parseOps(fl, args); help || err != nil {
		return err
	}
	if *from == "" {
		return errors.New("pass -from, the backup folder")
	}
	m, err := backup.ReadManifest(*from)
	if err != nil {
		return err
	}
	if latest := store.LatestSchemaVersion(); m.Schema > latest {
		return fmt.Errorf("the backup was made by mimir-server %s, whose database is newer than this version knows (schema %d, this one %d); restore it with %s or later", m.Version, m.Schema, latest, m.Version)
	}
	t, err := o.resolve()
	if err != nil {
		return err
	}
	switch {
	case m.KeyID == "":
	case t.key == nil:
		return fmt.Errorf("no secret key: put the key the backup was made with in %s, or set $%s or -secret-key-file; without it the secrets in the backup can't be read", filepath.Join(*o.data, secret.DefaultFile), secret.Env)
	case secret.KeyID(t.key) != m.KeyID:
		return errors.New("the secret key is not the one the backup was made with, so the secrets in it couldn't be read; use that key")
	}
	if m.LogDatabase && !t.separateLog() {
		return errors.New("the backup has a separate audit log database: pass -log-database-url (or set it in the configuration) for an empty database to restore it into")
	}
	ctx, stop, log := opsContext()
	defer stop()
	if !m.LogDatabase && t.separateLog() {
		log.Warn("the backup has no separate log database; its audit log is restored into the main one")
	}

	type target struct {
		name, url, dump string
		log             bool
	}
	targets := []target{{"main database", t.cfg.DatabaseURL, backup.MainDump, false}}
	if m.LogDatabase {
		targets = append(targets, target{"log database", t.cfg.LogDatabaseURL, backup.LogDump, true})
	}
	// Every check before the first write.
	for _, tg := range targets {
		if _, err := os.Stat(filepath.Join(*from, tg.dump)); err != nil {
			return fmt.Errorf("the backup's %s: %w", tg.name, err)
		}
		if *replace {
			continue
		}
		empty, err := store.Empty(ctx, tg.url)
		if err != nil {
			return fmt.Errorf("%s: %w", tg.name, err)
		}
		if !empty {
			return fmt.Errorf("the %s (%s) already has tables: restore into an empty database, or pass -replace to drop and replace what it holds", tg.name, store.Redact(tg.url))
		}
	}
	files := m.Files && !*noFiles
	for _, tg := range targets {
		if err := store.Restore(ctx, tg.url, filepath.Join(*from, tg.dump), *replace); err != nil {
			if *replace {
				return fmt.Errorf("%s: %w\n(nothing was changed; if it holds a newer version's tables, drop and recreate it empty and restore into it without -replace)", tg.name, err)
			}
			return fmt.Errorf("%s: %w", tg.name, err)
		}
		log.Info("restore: "+tg.name, "database", store.Redact(tg.url))
	}
	if files {
		n, err := backup.ExtractFiles(filepath.Join(*from, backup.FilesArchive), *o.data, *replace)
		if err != nil {
			return fmt.Errorf("the databases are restored, but the data folder isn't: %w", err)
		}
		log.Info("restore: data folder", "data", *o.data, "files", n)
	}
	// Restoring over a newer version's database leaves what only it had,
	// which this version would refuse to drop at start.
	if *replace {
		for _, tg := range targets {
			steps, err := store.Plan(ctx, tg.url, tg.log)
			if err != nil {
				return fmt.Errorf("%s: %w", tg.name, err)
			}
			for _, s := range steps {
				if s.Refused {
					return fmt.Errorf("the %s still holds what this version doesn't know (%s), most likely from a newer version, and the console won't start on it: drop and recreate the database empty (dropdb, createdb), then restore into it", tg.name, s.Why)
				}
			}
		}
	}
	fmt.Fprintf(os.Stderr, "Restored the backup mimir-server %s made at %s. Start the console; it brings the schema up to date.\n", m.Version, m.Created.Format(time.RFC3339))
	return nil
}

// runMigrate is `mimir-server migrate`: bring the databases up to this
// version's schema without starting, or with -check only say what that
// would change.
func runMigrate(args []string) error {
	fl := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fl.Usage = func() {
		fmt.Fprintln(fl.Output(), "Usage: mimir-server migrate [-check] [flags]")
		fmt.Fprintln(fl.Output(), "\nBrings the databases up to this version's schema, as the console does when it starts. With -check it only prints the changes it would make, and fails when the console would refuse one because it could lose data.")
		fl.PrintDefaults()
	}
	o := addOpsFlags(fl)
	check := fl.Bool("check", false, "print the changes without making them")
	if help, err := parseOps(fl, args); help || err != nil {
		return err
	}
	t, err := o.resolve()
	if err != nil {
		return err
	}
	ctx, stop, log := opsContext()
	defer stop()

	if *check {
		refused := 0
		type database struct {
			name, url string
			log       bool
		}
		dbs := []database{{"main database", t.cfg.DatabaseURL, false}}
		if t.separateLog() {
			dbs = append(dbs, database{"log database", t.cfg.LogDatabaseURL, true})
		}
		for _, db := range dbs {
			steps, err := store.Plan(ctx, db.url, db.log)
			if err != nil {
				return fmt.Errorf("%s: %w", db.name, err)
			}
			if len(steps) == 0 {
				fmt.Printf("-- %s (%s): up to date\n", db.name, store.Redact(db.url))
				continue
			}
			fmt.Printf("-- %s (%s): %d change(s)\n", db.name, store.Redact(db.url), len(steps))
			for _, s := range steps {
				if s.Refused {
					refused++
					fmt.Printf("-- REFUSED, could lose data: %s\n", s.Why)
				}
				fmt.Println(strings.TrimSpace(s.SQL))
			}
		}
		if refused > 0 {
			return fmt.Errorf("the console would refuse %d change(s) that could lose data and not start; is this database from a newer version?", refused)
		}
		return nil
	}

	if t.key == nil {
		return fmt.Errorf("no secret key: put it in %s, or set $%s or -secret-key-file", filepath.Join(*o.data, secret.DefaultFile), secret.Env)
	}
	opts := []store.Option{store.WithSecretKey(t.key)}
	if t.separateLog() {
		opts = append(opts, store.WithLogDatabase(t.cfg.LogDatabaseURL))
	}
	st, err := store.Open(ctx, t.cfg.DatabaseURL, *o.data, opts...)
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("migrate: done", "migration_steps", st.Applied(), "secrets_encrypted", st.Sealed())
	return nil
}
