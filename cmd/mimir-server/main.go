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
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/obs"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/secret"
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
	maxModelMB := flag.Int("max-model-mb", 256, "console: the largest model file an upload may be, in MB; a GGUF language model is often larger, and an upload needs about three times its size in memory while it is converted")
	trustedProxies := flag.String("trusted-proxies", os.Getenv("MIMIR_TRUSTED_PROXIES"), "console: comma-separated addresses and CIDR ranges of reverse proxies whose X-Forwarded-For and X-Forwarded-Proto headers to believe, such as 10.0.0.0/8,127.0.0.1; by default no one's are ($MIMIR_TRUSTED_PROXIES)")
	secretKeyFile := flag.String("secret-key-file", os.Getenv("MIMIR_SECRET_KEY_FILE"), "console: file holding the key stored secrets are encrypted with, 32 bytes as base64 or hex ($MIMIR_SECRET_KEY_FILE); $MIMIR_SECRET_KEY, the key itself, comes first; with neither, the key is <data>/secret.key, made on first start")
	metricsToken := flag.String("metrics-token", os.Getenv("MIMIR_METRICS_TOKEN"), "console: require this bearer token to read /metrics ($MIMIR_METRICS_TOKEN); empty serves it to anyone who can reach the port")
	insecure := flag.Bool("insecure", false, "worker: allow an http:// console on another machine (a private network you trust)")
	env := envDefaults{}
	predictTimeout := flag.Duration("predict-timeout", env.duration("MIMIR_PREDICT_TIMEOUT", api.DefaultPredictTimeout), "console: the longest one prediction may take, loading the model included, here or on a worker ($MIMIR_PREDICT_TIMEOUT)")
	predictBatch := flag.Int("predict-batch", env.int("MIMIR_PREDICT_BATCH", runtime.DefaultBatching.Max), "console and worker: the most predictions to one model to run together as one batch when they queue up; 1 runs each alone ($MIMIR_PREDICT_BATCH)")
	predictBatchWait := flag.Duration("predict-batch-wait", env.duration("MIMIR_PREDICT_BATCH_WAIT", runtime.DefaultBatching.Wait), "console and worker: how long a runner that found fewer than -predict-batch predictions waiting holds them for more before it runs; 0 runs at once ($MIMIR_PREDICT_BATCH_WAIT)")
	workerIdle := flag.Duration("worker-idle-timeout", env.duration("MIMIR_WORKER_IDLE_TIMEOUT", cluster.DefaultIdleTimeout), "console: how long a worker may go silent (it heartbeats every 15s) before its link is dropped and its tasks fail ($MIMIR_WORKER_IDLE_TIMEOUT)")
	cacheMB := flag.Int("model-cache-mb", env.int("MIMIR_MODEL_CACHE_MB", api.DefaultModelCacheBytes>>20), "console and worker: how many MB of models to keep loaded, by file size; the least recently used are dropped first, and the last one loaded always stays; 0 for no limit ($MIMIR_MODEL_CACHE_MB)")
	maxUploads := flag.Int("max-uploads", env.int("MIMIR_MAX_UPLOADS", server.DefaultMaxUploads), "console: how many model and dataset uploads to take at once; each is held in memory, so more are refused until one finishes ($MIMIR_MAX_UPLOADS)")
	trainingJobs := flag.Int("training-jobs", env.int("MIMIR_TRAINING_JOBS", 0), "console: how many training jobs run at once (default: one per training slot of the connected workers, plus one for a standalone server, at least 2) ($MIMIR_TRAINING_JOBS)")
	webhookWorkers := flag.Int("webhook-workers", env.int("MIMIR_WEBHOOK_WORKERS", 0), "console: how many webhooks are sent to at once; each webhook still gets its events one at a time, in order (default 8) ($MIMIR_WEBHOOK_WORKERS)")
	flag.Parse()
	if err := env.err(); err != nil {
		return err
	}
	if *trainingJobs < 0 || *webhookWorkers < 0 {
		return errors.New("-training-jobs and -webhook-workers can't be negative")
	}

	// Lines logged with a request's context carry its request id.
	log := slog.New(obs.NewLogHandler(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *role {
	case "worker":
		if *serverURL == "" || *token == "" {
			return errors.New("a worker needs -server and -token (or $MIMIR_WORKER_TOKEN)")
		}
		log.Info("worker: starting", "server", *serverURL, "data", *data, "version", version)
		if err := os.MkdirAll(*data, 0o700); err != nil {
			return err
		}
		cacheBytes := int64(*cacheMB) << 20
		if cacheBytes <= 0 {
			cacheBytes = -1 // no limit
		}
		err := worker.Run(ctx, worker.Config{Server: *serverURL, Token: *token, Name: *name, Capacity: *capacity, Version: version, Log: log, DataDir: *data, Insecure: *insecure, CacheBytes: cacheBytes}, builtin.Builtin(*data))
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

	proxies, err := auth.ParseProxies(*trustedProxies)
	if err != nil {
		return fmt.Errorf("-trusted-proxies: %w", err)
	}

	if *databaseURL == "" {
		return errors.New("the console keeps its records in PostgreSQL: pass -database-url or set $MIMIR_DATABASE_URL, such as postgres://mimir:secret@localhost:5432/mimir")
	}
	key, keySource, err := secret.Load(os.Getenv(secret.Env), *secretKeyFile, *data)
	if err != nil {
		return err
	}
	if keySource.Generated {
		log.Warn("secret key: made a new key for encrypting stored secrets; back it up with the database, which cannot be read without it", "path", keySource.Where)
	} else {
		log.Info("secret key: loaded", "from", keySource.Where)
	}
	st, err := store.Open(ctx, *databaseURL, *data, store.WithSecretKey(key))
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("database ready", "database", store.Redact(*databaseURL), "migration_steps", st.Applied(), "secrets_encrypted", st.Sealed())

	if *predictBatch < 1 || *predictBatchWait < 0 {
		log.Error("-predict-batch must be at least 1 and -predict-batch-wait not negative")
		os.Exit(2)
	}
	runtime.SetDefaultBatching(runtime.Batching{Max: *predictBatch, Wait: *predictBatchWait})
	svc := api.New(st, log)
	if err := svc.LoadArchitectures(ctx); err != nil {
		log.Error("couldn't read the described model types", "error", err)
		os.Exit(1)
	}
	svc.AllowPrivateWebhooks = *allowPrivate
	svc.Pool = cluster.New(log)
	svc.Pool.IdleTimeout = *workerIdle
	svc.PredictTimeout = *predictTimeout
	svc.MaxUploads = *maxUploads
	svc.SetModelCacheBytes(int64(max(*cacheMB, 0)) << 20)
	svc.Pool.OnSample = svc.KeepSample
	svc.Local = local
	svc.MaxModelBytes = *maxModelMB << 20
	svc.AllowPlainWorkers = *plainWorkers
	svc.Proxies = proxies
	if proxies.Any() {
		log.Info("trusting forwarded headers", "proxies", proxies.String())
	}
	tlsConfig, pin, err := server.TLS(*tlsCert, *tlsKey, *tlsAuto, *data)
	if err != nil {
		return err
	}
	svc.ConsolePin = pin
	// With no accounts yet, the first owner needs a one-time token that
	// only someone who can read this log (or set the environment) knows.
	presetSetup := os.Getenv("MIMIR_SETUP_TOKEN")
	setupToken, err := svc.PrepareSetup(ctx, presetSetup)
	if err != nil {
		return fmt.Errorf("$MIMIR_SETUP_TOKEN: %w", err)
	}
	switch {
	case setupToken == "":
	case presetSetup != "":
		log.Info("no accounts yet: open the console and set it up with the token in $MIMIR_SETUP_TOKEN")
	default:
		log.Info("no accounts yet: open the console and set it up with this one-time setup token", "setup_token", setupToken)
	}

	// Webhook deliveries and housekeeping run in the background until the
	// server stops.
	var bg sync.WaitGroup
	bgCtx, stopBg := context.WithCancel(context.Background())
	// Deferred after st.Close, so it runs first: on every way out the
	// background work stops and finishes, including the last usage flush,
	// before the database closes under it.
	defer func() {
		stopBg()
		bg.Wait()
	}()
	dispatcher := webhook.New(st, log, *allowPrivate)
	dispatcher.OnGiveUp = func(ctx context.Context, w store.Webhook, d store.Delivery) {
		svc.Notifier.Notify(ctx, "Webhook "+w.Name+" is failing",
			fmt.Sprintf("mimir gave up sending a %s event to %s after %d tries.\nLast error: %s\n\nCheck the endpoint, then use Send again under Connect your apps.", d.EventType, w.URL, d.Attempts, d.Error))
	}
	dispatcher.Workers = *webhookWorkers
	svc.Kick = dispatcher.Kick
	bg.Go(func() { dispatcher.Run(bgCtx, 5*time.Second) })
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

	// Training runs several jobs at once; shutdown waits for them. A job cut
	// short by shutdown goes back in the queue and resumes from its
	// checkpoint.
	runner := training.New(st, log)
	runner.Jobs = *trainingJobs
	runner.Local = local
	runner.Remote = svc.Pool
	runner.OnDone = svc.TrainingDone
	svc.KickTraining = runner.Kick
	svc.CancelTraining = runner.Cancel
	bg.Go(func() { runner.Run(bgCtx) })

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(svc, st, web.Dist(), log, server.WithMetricsToken(*metricsToken)),
		Protocols:         server.Protocols(),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr, "data", *data, "role", *role, "tls", tlsConfig != nil, "version", version)
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
	// A connected worker's link never ends on its own, so Shutdown runs out
	// of time whenever one is connected; close what is left and carry on.
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("shutdown: closing connections still open", "error", err)
		srv.Close()
	}
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

// envDefaults reads flag defaults from the environment, remembering the
// first value it couldn't parse.
type envDefaults struct{ bad error }

func (e *envDefaults) duration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.fail(fmt.Errorf("$%s=%q: want a duration such as 30s or 2m", name, v))
		return def
	}
	return d
}

func (e *envDefaults) int(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		e.fail(fmt.Errorf("$%s=%q: want a whole number", name, v))
		return def
	}
	return n
}

func (e *envDefaults) fail(err error) {
	if e.bad == nil {
		e.bad = err
	}
}

func (e *envDefaults) err() error { return e.bad }
