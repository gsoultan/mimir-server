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
	"github.com/gsoultan/mimir-server/internal/server"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
	"github.com/gsoultan/mimir-server/internal/webhook"
	"github.com/gsoultan/mimir-server/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mimir-server:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":8080", "address to listen on")
	data := flag.String("data", "./data", "directory for the database and model files")
	allowPrivate := flag.Bool("allow-private-webhooks", false, "let webhooks point at local and private addresses (for development)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, *data)
	if err != nil {
		return err
	}
	defer st.Close()

	svc := api.New(st, log)
	svc.AllowPrivateWebhooks = *allowPrivate

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

	// Training runs one job at a time in this process. A job cut short by
	// shutdown goes back in the queue and resumes from its checkpoint.
	runner := training.New(st, log)
	runner.OnDone = svc.TrainingDone
	svc.KickTraining = runner.Kick
	svc.CancelTraining = runner.Cancel
	bg.Go(func() { runner.Run(bgCtx) })

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(svc, st, web.Dist(), log),
		Protocols:         server.Protocols(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr, "data", *data)
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
