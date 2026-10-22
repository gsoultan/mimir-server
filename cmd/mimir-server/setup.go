package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/config"
	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/setup"
	"github.com/gsoultan/mimir-server/internal/store"
)

// runSetup is `mimir-server setup`: first-run setup without the web
// screen, for installs scripted or done over SSH.
func runSetup(args []string) error {
	fl := flag.NewFlagSet("setup", flag.ContinueOnError)
	fl.Usage = func() {
		fmt.Fprintln(fl.Output(), "Usage: mimir-server setup -database-url URL -owner-email EMAIL -owner-name NAME [flags]")
		fmt.Fprintln(fl.Output(), "\nWrites the configuration file and makes the first owner, as the setup screen does. The owner's password comes from $MIMIR_OWNER_PASSWORD or -owner-password-file (- for standard input), never a flag.")
		fl.PrintDefaults()
	}
	data := fl.String("data", "./data", "the data folder, as the server is started with")
	configFile := fl.String("config", os.Getenv(config.Env), "the configuration file to write ($MIMIR_CONFIG); default <data>/mimir.json")
	databaseURL := fl.String("database-url", os.Getenv("MIMIR_DATABASE_URL"), "the PostgreSQL database for the server's records ($MIMIR_DATABASE_URL); env:NAME or file:PATH keeps it out of the file")
	logDatabaseURL := fl.String("log-database-url", os.Getenv("MIMIR_LOG_DATABASE_URL"), "a second PostgreSQL database for the audit log ($MIMIR_LOG_DATABASE_URL); empty keeps it in the main one")
	secretKeyFile := fl.String("secret-key-file", os.Getenv("MIMIR_SECRET_KEY_FILE"), "the server's secret key file, when it isn't the data folder's ($MIMIR_SECRET_KEY_FILE); $MIMIR_SECRET_KEY comes first")
	importKey := fl.String("import-key-file", "", "with no key yet, a file holding the key to keep (such as one backed up with the database) instead of making a new one")
	auditDays := fl.Int("audit-days", api.DefaultRetention.AuditDays, "days to keep audit events; 0 keeps them forever")
	deliveryDays := fl.Int("delivery-days", api.DefaultRetention.DeliveryDays, "days to keep webhook delivery history")
	email := fl.String("owner-email", "", "the first owner's email; not needed when the database already has accounts")
	name := fl.String("owner-name", "", "the first owner's name")
	passwordFile := fl.String("owner-password-file", "", "a file holding the first owner's password, or - to read it from standard input ($MIMIR_OWNER_PASSWORD comes first)")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fl.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fl.Arg(0))
	}
	if *databaseURL == "" {
		return errors.New("pass -database-url or set $MIMIR_DATABASE_URL")
	}
	cfgPath := config.Path(*configFile, *data)
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("%s is already there: this server is set up", cfgPath)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	key, err := setup.FindKey(os.Getenv(secret.Env), *secretKeyFile, *data)
	if err != nil {
		return err
	}
	p := setup.Plan{DataDir: *data, ConfigPath: cfgPath, DatabaseURL: *databaseURL, LogDatabaseURL: *logDatabaseURL, Key: key}
	if *importKey != "" {
		if key.Source != setup.KeyNone {
			return errors.New("-import-key-file: the server already has a key; leave the flag out")
		}
		b, err := os.ReadFile(*importKey)
		if err != nil {
			return err
		}
		if p.NewKey, err = secret.ParseKey(string(b)); err != nil {
			return fmt.Errorf("-import-key-file: %w", err)
		}
	}
	r, err := api.CheckRetention(*auditDays, *deliveryDays)
	if err != nil {
		return errors.New("keep audit events for 0 (forever) to 3650 days, and deliveries for 1 to 3650 days")
	}
	p.Retention = &r

	dbURL, err := config.Resolve(nil, *databaseURL, "database_url")
	if err != nil {
		return err
	}
	if err := setup.CheckURL(dbURL, "database URL"); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	version, has, err := store.CheckDatabase(cctx, dbURL)
	cancel()
	if err != nil {
		return errors.New(setup.Scrub(err.Error(), dbURL))
	}
	log.Info("database answers", "database", store.Redact(dbURL), "version", version, "has_accounts", has)
	if !has {
		if *email == "" || *name == "" {
			return errors.New("the database has no accounts yet: pass -owner-email and -owner-name for the first owner")
		}
		password, err := readPassword(*passwordFile)
		if err != nil {
			return err
		}
		hash, e, n, err := api.NewOwner(*email, *name, password)
		if err != nil {
			return err
		}
		p.Owner = &setup.Owner{Email: e, Name: n, PasswordHash: hash}
	}

	res, err := setup.Apply(ctx, p)
	if err != nil {
		return errors.New(setup.Scrub(err.Error(), dbURL))
	}
	log.Info("setup: done", "config", cfgPath, "owner_created", res.OwnerCreated, "key_generated", res.KeyGenerated, "migration_steps", res.MigrationSteps)
	if res.KeySaved {
		log.Warn("secret key: saved; back it up with the database, which cannot be read without it", "path", key.File)
	}
	fmt.Fprintf(os.Stderr, "Set up. Start the server with -data %s and sign in.\n", *data)
	return nil
}

// readPassword takes the owner's password from $MIMIR_OWNER_PASSWORD, or a
// file, or standard input for "-". Only its first line counts.
func readPassword(file string) (string, error) {
	if v := os.Getenv("MIMIR_OWNER_PASSWORD"); v != "" {
		return v, nil
	}
	var in io.Reader
	switch file {
	case "":
		return "", errors.New("give the first owner's password in $MIMIR_OWNER_PASSWORD or with -owner-password-file")
	case "-":
		in = os.Stdin
	default:
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		in = f
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
