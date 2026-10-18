// Command mimir-server serves mimir models over Connect, gRPC and a web UI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/server"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
	"github.com/gsoultan/mimir-server/internal/webhook"
	builtin "github.com/gsoultan/mimir-server/internal/worker"
	"github.com/gsoultan/mimir-server/web"
	"github.com/gsoultan/mimir-server/worker"
)

// version is reported by workers; set with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mimir-server:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":8080", "address to listen on")
	data := flag.String("data", "./data", "directory for model weights, datasets and training checkpoints")
	databaseURL := flag.String("database-url", os.Getenv("MIMIR_DATABASE_URL"), "console: the PostgreSQL database for the server's own records, such as postgres://mimir:secret@localhost:5432/mimir ($MIMIR_DATABASE_URL)")
	allowPrivate := flag.Bool("allow-private-webhooks", false, "let webhooks point at local and private addresses (for development)")
	role := flag.String("role", "standalone", "standalone (the console and a worker in one), console (the web app and API; workers run the models) or worker (runs models for a console)")
	tlsCert := flag.String("tls-cert", "", "console: serve TLS with this certificate file")
	tlsKey := flag.String("tls-key", "", "console: the key for -tls-cert")
	tlsAuto := flag.Bool("tls-auto", false, "console: serve TLS with a certificate made in the data folder; workers check it by fingerprint")
	plainWorkers := flag.Bool("allow-plain-workers", false, "console: let workers on other machines connect without encryption (a private network you trust)")
	serverURL := flag.String("server", "", "worker: the console to join, such as https://mimir.example.com")
	token := flag.String("token", os.Getenv("MIMIR_WORKER_TOKEN"), "worker: the token made under Settings → Workers ($MIMIR_WORKER_TOKEN)")
	name := flag.String("name", "", "worker: a name for the logs")
	capacity := flag.Int("capacity", 0, "worker: how many tasks to run at once (default: the CPU count)")
	insecure := flag.Bool("insecure", false, "worker: allow an http:// console on another machine (a private network you trust)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *role {
	case "worker":
		if *serverURL == "" || *token == "" {
			return errors.New("a worker needs -server and -token (or $MIMIR_WORKER_TOKEN)")
		}
		log.Info("worker: starting", "server", *serverURL, "data", *data)
		if err := os.MkdirAll(*data, 0o700); err != nil {
			return err
		}
		err := worker.Run(ctx, worker.Config{Server: *serverURL, Token: *token, Name: *name, Capacity: *capacity, Version: version, Log: log, DataDir: *data, Insecure: *insecure}, builtin.Builtin(*data))
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case "server": // the old name
		*role = "console"
	case "standalone", "console":
	default:
		return fmt.Errorf("unknown -role %q: use standalone, console or worker", *role)
	}
	local := *role == "standalone"

	if *databaseURL == "" {
		return errors.New("the console keeps its records in PostgreSQL: pass -database-url or set $MIMIR_DATABASE_URL, such as postgres://mimir:secret@localhost:5432/mimir")
	}
	st, err := store.Open(ctx, *databaseURL, *data)
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("database ready", "database", store.Redact(*databaseURL), "migration_steps", st.Applied())

	svc := api.New(st, log)
	svc.AllowPrivateWebhooks = *allowPrivate
	svc.Pool = cluster.New(log)
	svc.Pool.OnSample = svc.KeepSample
	svc.Local = local
	svc.AllowPlainWorkers = *plainWorkers
	tlsConfig, pin, err := server.TLS(*tlsCert, *tlsKey, *tlsAuto, *data)
	if err != nil {
		return err
	}
	svc.ConsolePin = pin

	// Webhook deliveries and housekeeping run in the background until the
	// server stops.
	var bg sync.WaitGroup
	bgCtx, stopBg := context.WithCancel(context.Background())
	defer stopBg()
	dispatcher := webhook.New(st, log, *allowPrivate)
	dispatcher.OnGiveUp = func(ctx context.Context, w store.Webhook, d store.Delivery) {
		svc.Notifier.Notify(ctx, "Webhook "+w.Name+" is failing",
			fmt.Sprintf("mimir gave up sending a %s event to %s after %d tries.\nLast error: %s\n\nCheck the endpoint, then use Send again under Connect your apps.", d.EventType, w.URL, d.Attempts, d.Error))
	}
	kick := make(chan struct{}, 1)
	svc.Kick = func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	bg.Go(func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			dispatcher.Tick(bgCtx)
			select {
			case <-bgCtx.Done():
				return
			case <-kick:
			case <-t.C:
			}
		}
	})
	bg.Go(func() { janitor(bgCtx, st, log) })
	// Prediction counts are written in batches, and once more on the way out.
	bg.Go(func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-bgCtx.Done():
				if err := svc.FlushUsage(context.Background()); err != nil {
					log.Error("usage: flush", "error", err)
				}
				return
			case <-t.C:
				if err := svc.FlushUsage(bgCtx); err != nil {
					log.Error("usage: flush", "error", err)
				}
			}
		}
	})

	// Training runs one job at a time in this process. A job cut short by
	// shutdown goes back in the queue and resumes from its checkpoint.
	runner := training.New(st, log)
	runner.Local = local
	runner.Remote = svc.Pool
	runner.OnDone = svc.TrainingDone
	svc.KickTraining = runner.Kick
	svc.CancelTraining = runner.Cancel
	bg.Go(func() { runner.Run(bgCtx) })

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(svc, st, web.Dist(), log),
		Protocols:         server.Protocols(),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr, "data", *data, "role", *role, "tls", tlsConfig != nil)
		if tlsConfig != nil {
			errc <- srv.ListenAndServeTLS("", "")
			return
		}
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	stopBg()
	bg.Wait()
	return nil
}

// janitor trims old history once an hour, by the retention settings.
func janitor(ctx context.Context, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		r, err := api.LoadRetention(ctx, st)
		if err != nil {
			log.ErrorContext(ctx, "janitor: retention", "error", err)
		} else {
			audit := time.Duration(r.AuditDays) * 24 * time.Hour
			if r.AuditDays == 0 {
				audit = 100 * 365 * 24 * time.Hour // forever, as far as anyone cares
			}
			n, err := st.Trim(ctx, audit, time.Duration(r.DeliveryDays)*24*time.Hour)
			if err != nil {
				log.ErrorContext(ctx, "janitor: trim", "error", err)
			} else {
				rec, _ := json.Marshal(api.TrimRecord{Time: time.Now().UTC(), Rows: n})
				st.SetSetting(ctx, api.KeyTrim, string(rec))
				log.InfoContext(ctx, "janitor: trimmed", "rows", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
