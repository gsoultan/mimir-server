// Command mimir-server serves mimir models over Connect, gRPC and a web UI.
package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gsoultan/mimir/backend/cpu"

	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/config"
	"github.com/gsoultan/mimir-server/internal/obs"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/server"
	"github.com/gsoultan/mimir-server/internal/setup"
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
	if len(os.Args) > 1 {
		if cmd, ok := commands[os.Args[1]]; ok {
			if err := cmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "mimir-server %s: %v\n", os.Args[1], err)
				os.Exit(1)
			}
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mimir-server:", err)
		os.Exit(1)
	}
}

// commands are the ways to run mimir-server other than serving.
var commands = map[string]func(args []string) error{
	"setup":   runSetup,
	"backup":  runBackup,
	"restore": runRestore,
	"migrate": runMigrate,
}

func run() error {
	addr := flag.String("addr", ":8080", "address to listen on")
	data := flag.String("data", "./data", "directory for model weights, datasets and training checkpoints")
	databaseURL := flag.String("database-url", os.Getenv("MIMIR_DATABASE_URL"), "console: the PostgreSQL database for the server's own records, such as postgres://mimir:secret@localhost:5432/mimir ($MIMIR_DATABASE_URL); without it the configuration file's is used, and without that the console opens first-run setup")
	logDatabaseURL := flag.String("log-database-url", os.Getenv("MIMIR_LOG_DATABASE_URL"), "console: a second PostgreSQL database for the audit log; empty keeps it in the main one ($MIMIR_LOG_DATABASE_URL)")
	configFile := flag.String("config", os.Getenv(config.Env), "console: the configuration file first-run setup writes and later starts read ($MIMIR_CONFIG); default <data>/mimir.json. Flags and environment variables come first")
	allowPrivate := flag.Bool("allow-private-webhooks", false, "let webhooks point at local and private addresses (for development)")
	role := flag.String("role", "standalone", "standalone (the console and a worker in one), console (the web app and API; workers run the models) or worker (runs models for a console)")
	tasks := flag.String("tasks", os.Getenv("MIMIR_WORKER_TASKS"), "worker: the work it takes: all (predictions and training, the default), serve (predictions only) or train (training runs only), so training machines and serving machines stay apart ($MIMIR_WORKER_TASKS)")
	tlsCert := flag.String("tls-cert", "", "console: serve TLS with this certificate file")
	tlsKey := flag.String("tls-key", "", "console: the key for -tls-cert")
	tlsAuto := flag.Bool("tls-auto", false, "console: serve TLS with a certificate made in the data folder; workers check it by fingerprint")
	plainWorkers := flag.Bool("allow-plain-workers", false, "console: let workers on other machines connect without encryption (a private network you trust)")
	serverURL := flag.String("server", "", "worker: the console to join, such as https://mimir.example.com")
	token := flag.String("token", os.Getenv("MIMIR_WORKER_TOKEN"), "worker: the token made under Settings → Workers ($MIMIR_WORKER_TOKEN)")
	name := flag.String("name", "", "worker: a name for the logs")
	capacity := flag.Int("capacity", 0, "worker: how many tasks to run at once (default: the CPU count)")
	maxModelMB := flag.Int("max-model-mb", 256, "console: the largest model file an upload may be, in MB; a GGUF language model is often larger. The console streams uploads to disk, so their size takes disk rather than memory")
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
	trainingJobs := flag.Int("training-jobs", env.int("MIMIR_TRAINING_JOBS", 0), "console: how many training jobs run at once (default: one per training slot of the connected workers, plus one for a standalone server that trains here, at least 2) ($MIMIR_TRAINING_JOBS)")
	trainHere := flag.Bool("train-here", env.bool("MIMIR_TRAIN_HERE", true), "standalone: train runs in this process when no worker takes them, until Settings → Workers says otherwise; false leaves every run to workers, so training never slows the API served here ($MIMIR_TRAIN_HERE)")
	webhookWorkers := flag.Int("webhook-workers", env.int("MIMIR_WEBHOOK_WORKERS", 0), "console: how many webhooks are sent to at once; each webhook still gets its events one at a time, in order (default 8) ($MIMIR_WEBHOOK_WORKERS)")
	bf16 := flag.Bool("bf16", env.bool("MIMIR_BF16", false), "console and worker: train and run models with bfloat16 matrix products on CPUs with Intel AMX (Xeon from Sapphire Rapids on), about 1.6 times faster training of deep table models for a tenth of a point of accuracy or less; elsewhere it changes nothing ($MIMIR_BF16)")
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
	if *bf16 {
		if cpu.SetMatMulBFloat16(true) {
			log.Info("bf16: matrix products in bfloat16 on AMX")
		} else {
			log.Warn("bf16: this CPU has no AMX, so matrix products stay in float32")
		}
	}

	switch *role {
	case "worker":
		if *serverURL == "" || *token == "" {
			return errors.New("a worker needs -server and -token (or $MIMIR_WORKER_TOKEN)")
		}
		log.Info("worker: starting", "server", *serverURL, "data", *data, "tasks", cmp.Or(*tasks, "all"), "version", version)
		if err := os.MkdirAll(*data, 0o700); err != nil {
			return err
		}
		cacheBytes := int64(*cacheMB) << 20
		if cacheBytes <= 0 {
			cacheBytes = -1 // no limit
		}
		kind, err := worker.ParseTasks(*tasks)
		if err != nil {
			return fmt.Errorf("-tasks: %w", err)
		}
		err = worker.Run(ctx, worker.Config{Server: *serverURL, Token: *token, Name: *name, Capacity: *capacity, Tasks: kind, Version: version, Log: log, DataDir: *data, Insecure: *insecure, CacheBytes: cacheBytes}, builtin.Builtin(*data))
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
	// A standalone console answers predictions itself; whether it also
	// trains here is a setting (Settings → Workers), -train-here being
	// its default, so training can be kept off the machine that serves
	// the API.

	proxies, err := auth.ParseProxies(*trustedProxies)
	if err != nil {
		return fmt.Errorf("-trusted-proxies: %w", err)
	}

	tlsConfig, pin, err := server.TLS(*tlsCert, *tlsKey, *tlsAuto, *data)
	if err != nil {
		return err
	}
	// The listener starts early when first-run setup needs it, and the full
	// handler takes its place once setup is done, on the same port.
	var handler atomic.Pointer[http.Handler]
	srv := &http.Server{
		Addr:              *addr,
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*handler.Load()).ServeHTTP(w, r) }),
		Protocols:         server.Protocols(),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	var listening sync.Once
	listen := func() {
		listening.Do(func() {
			go func() {
				log.Info("listening", "addr", *addr, "data", *data, "role", *role, "tls", tlsConfig != nil, "version", version)
				if tlsConfig != nil {
					errc <- srv.ListenAndServeTLS("", "")
					return
				}
				errc <- srv.ListenAndServe()
			}()
		})
	}

	// Flags and the environment come first, then the configuration file;
	// with neither the console has no database yet and opens setup.
	cfgPath := config.Path(*configFile, *data)
	cfg := config.File{DatabaseURL: *databaseURL, LogDatabaseURL: *logDatabaseURL, SecretKeyFile: *secretKeyFile}
	if cfg.DatabaseURL == "" {
		f, err := config.Read(cfgPath)
		switch {
		case err == nil:
			log.Info("configuration: loaded", "path", cfgPath)
			cfg.DatabaseURL = f.DatabaseURL
			cfg.LogDatabaseURL = cmp.Or(cfg.LogDatabaseURL, f.LogDatabaseURL)
			cfg.SecretKeyFile = cmp.Or(cfg.SecretKeyFile, f.SecretKeyFile)
		case errors.Is(err, fs.ErrNotExist):
			done, err := firstRun(ctx, log, &handler, listen, errc, cfgPath, *data, *secretKeyFile, proxies)
			if err != nil {
				return err
			}
			if done == nil { // stopped before setup finished
				srv.Close()
				return nil
			}
			cfg = *done
		default:
			return err
		}
	}
	key, keySource, err := secret.Load(os.Getenv(secret.Env), cfg.SecretKeyFile, *data)
	if err != nil {
		return err
	}
	if keySource.Generated {
		log.Warn("secret key: made a new key for encrypting stored secrets; back it up with the database, which cannot be read without it", "path", keySource.Where)
	} else {
		log.Info("secret key: loaded", "from", keySource.Where)
	}
	box, err := secret.New(key)
	if err != nil {
		return err
	}
	if cfg, err = cfg.Open(box); err != nil {
		return err
	}
	storeOpts := []store.Option{store.WithSecretKey(key)}
	if cfg.LogDatabaseURL != "" && cfg.LogDatabaseURL != cfg.DatabaseURL {
		storeOpts = append(storeOpts, store.WithLogDatabase(cfg.LogDatabaseURL))
	}
	st, err := store.Open(ctx, cfg.DatabaseURL, *data, storeOpts...)
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("database ready", "database", store.Redact(cfg.DatabaseURL), "log_database", redactOrNone(cfg.LogDatabaseURL), "migration_steps", st.Applied(), "secrets_encrypted", st.Sealed())

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
	svc.Pool.OnLink = svc.WorkerLinked
	svc.Local = local
	svc.TrainHereDefault = *trainHere
	if err := svc.LoadTrainingSetting(ctx); err != nil {
		return fmt.Errorf("training setting: %w", err)
	}
	svc.MaxModelBytes = *maxModelMB << 20
	svc.AllowPlainWorkers = *plainWorkers
	svc.Proxies = proxies
	if proxies.Any() {
		log.Info("trusting forwarded headers", "proxies", proxies.String())
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
	runner.TrainHere = svc.TrainsHere
	runner.Remote = svc.Pool
	runner.OnDone = svc.TrainingDone
	svc.KickTraining = runner.Kick
	svc.CancelTraining = runner.Cancel
	bg.Go(func() { runner.Run(bgCtx) })

	if *metricsToken == "" && !loopbackOnly(*addr) {
		log.Warn("/metrics is open to anyone who can reach this address; set -metrics-token ($MIMIR_METRICS_TOKEN) to require a token", "addr", *addr)
	}
	full := server.Handler(svc, st, web.Dist(), log, server.WithMetricsToken(*metricsToken))
	handler.Store(&full)
	listen()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// A connected worker's link never ends on its own; end them all as
	// Shutdown starts, so it only waits for ordinary requests. Anything
	// still open when it runs out of time is closed.
	srv.RegisterOnShutdown(svc.Pool.Close)
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("shutdown: closing connections still open", "error", err)
		srv.Close()
	}
	return nil
}

// firstRun serves first-run setup until it finishes, then returns the
// configuration it wrote. It returns nil when the server is stopped first.
func firstRun(ctx context.Context, log *slog.Logger, handler *atomic.Pointer[http.Handler], listen func(), errc <-chan error, cfgPath, dataDir, keyFile string, proxies auth.Proxies) (*config.File, error) {
	key, err := setup.FindKey(os.Getenv(secret.Env), keyFile, dataDir)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(os.Getenv("MIMIR_SETUP_TOKEN"))
	preset := token != ""
	switch {
	case !preset:
		token = rand.Text()
	case len(token) < api.MinSetupToken:
		return nil, fmt.Errorf("$MIMIR_SETUP_TOKEN: the setup token needs at least %d characters", api.MinSetupToken)
	}
	s := setup.NewServer(token)
	s.DataDir, s.ConfigPath, s.Key, s.Proxies, s.Log = dataDir, cfgPath, key, proxies, log
	h := server.SetupHandler(s, proxies, web.Dist(), log)
	handler.Store(&h)
	listen()
	if preset {
		log.Info("no configuration yet: open the console to set it up, with the token in $MIMIR_SETUP_TOKEN, or run `mimir-server setup`", "config", cfgPath)
	} else {
		log.Info("no configuration yet: open the console to set it up, with this one-time setup token, or run `mimir-server setup`", "config", cfgPath, "setup_token", token)
	}
	select {
	case f := <-s.Done:
		return &f, nil
	case err := <-errc:
		return nil, err
	case <-ctx.Done():
		return nil, nil
	}
}

func redactOrNone(u string) string {
	if u == "" {
		return ""
	}
	return store.Redact(u)
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

func (e *envDefaults) bool(name string, def bool) bool {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail(fmt.Errorf("$%s=%q: want true or false", name, v))
		return def
	}
	return b
}

func (e *envDefaults) fail(err error) {
	if e.bad == nil {
		e.bad = err
	}
}

func (e *envDefaults) err() error { return e.bad }

// loopbackOnly reports whether addr listens only on this machine's
// loopback, where an open /metrics reaches no one else.
func loopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
