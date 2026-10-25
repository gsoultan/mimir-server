//go:build livegpu

package gpucloud

// The live test rents a real GPU from one provider, for a few minutes, and
// checks the console's calls against the real API: Start, Find, Exited
// while it runs, Stop, and Exited and Find once it is gone. It is built
// only with the livegpu tag, so go test ./... never spends money:
//
//	MIMIR_LIVE_PROVIDER=runpod RUNPOD_API_KEY=... \
//	  go test -tags livegpu -run TestLiveProvider -v -timeout 40m ./internal/gpucloud
//
// The provider's keys come from the environment:
//
//	runpod  RUNPOD_API_KEY
//	vast    VAST_API_KEY
//	lambda  LAMBDA_API_KEY and LAMBDA_SSH_KEY (the name of an SSH key there)
//	aws     AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or the SDK's own
//	gcp     GOOGLE_APPLICATION_CREDENTIALS (a service account JSON key file)
//	modal   MODAL_TOKEN_ID and MODAL_TOKEN_SECRET
//
// MIMIR_LIVE_GPU and MIMIR_LIVE_REGION pick another GPU or region than the
// provider's default; MIMIR_LIVE_HOLD is how long the machine is watched
// while it runs (default 3m). The machine is removed when the test ends,
// even when it fails; a machine it could not remove is named in the log.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"testing"
	"time"
)

func liveConfig(t *testing.T) (Config, Cloud) {
	t.Helper()
	c := Config{Enabled: true, Provider: os.Getenv("MIMIR_LIVE_PROVIDER"), GPU: os.Getenv("MIMIR_LIVE_GPU"), Region: os.Getenv("MIMIR_LIVE_REGION")}
	var cl Cloud
	switch c.Provider {
	case ProviderRunPod:
		c.TokenSecret, cl = os.Getenv("RUNPOD_API_KEY"), NewRunPod()
	case ProviderVast:
		c.TokenSecret, cl = os.Getenv("VAST_API_KEY"), NewVast()
	case ProviderLambda:
		c.TokenSecret, c.SSHKey, cl = os.Getenv("LAMBDA_API_KEY"), os.Getenv("LAMBDA_SSH_KEY"), NewLambda()
	case ProviderAWS:
		c.TokenID, c.TokenSecret, cl = os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), NewAWS()
	case ProviderGCP:
		if f := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			c.TokenSecret = string(b)
		}
		cl = NewGCP()
	case ProviderModal:
		c.TokenID, c.TokenSecret, cl = os.Getenv("MODAL_TOKEN_ID"), os.Getenv("MODAL_TOKEN_SECRET"), NewModal(slog.Default())
	case "":
		t.Skip("set MIMIR_LIVE_PROVIDER to rent a GPU for real")
	default:
		t.Fatalf("no provider called %q", c.Provider)
	}
	c = c.WithDefaults()
	p, _ := ProviderByID(c.Provider)
	if err := c.checkProvider(p); err != nil {
		t.Fatal(err)
	}
	return c, cl
}

func TestLiveProvider(t *testing.T) {
	c, cl := liveConfig(t)
	hold := 3 * time.Minute
	if s := os.Getenv("MIMIR_LIVE_HOLD"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatal(err)
		}
		hold = d
	}
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	workerID := id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	m := Machine{
		WorkerID:  workerID,
		Name:      machineName(workerID),
		GPU:       c.GPU,
		Region:    c.Region,
		CPU:       1,
		MemoryMiB: 4 << 10,
		// The VMs power themselves off after this, should the test not
		// remove them.
		Timeout: 30 * time.Minute,
		Setup:   "nvidia-smi --query-gpu=name,memory.total --format=csv,noheader || true\necho \"live test: secret has ${#MIMIR_WORKER_SECRET} characters\"",
		Run:     "sleep 1800",
		Env:     map[string]string{"MIMIR_DEVICE": "gpu"},
		// Shaped like a real worker token, so a provider that mangles one
		// shows here.
		Secrets: map[string]string{"MIMIR_WORKER_SECRET": "mimirw_" + id + "." + id[:12]},
	}
	ctx := context.Background()
	t0 := time.Now()
	step := func(format string, args ...any) {
		t.Logf("%6.0fs  "+format, append([]any{time.Since(t0).Seconds()}, args...)...)
	}

	step("starting a %s on %s (%s), worker %s", c.GPU, c.Provider, c.Region, workerID)
	sctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	machine, err := cl.Start(sctx, c, m)
	cancel()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	step("started machine %s", machine)
	removed := false
	t.Cleanup(func() {
		if removed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := cl.Stop(ctx, c, machine); err != nil {
			t.Errorf("COULDN'T REMOVE machine %s on %s; remove it by hand: %v", machine, c.Provider, err)
			return
		}
		t.Logf("removed machine %s", machine)
	})

	// poll asks f every 15s until it says done, for at most d.
	poll := func(d time.Duration, f func() (bool, error)) error {
		end := time.Now().Add(d)
		for {
			done, err := f()
			if done || time.Now().After(end) {
				return err
			}
			time.Sleep(15 * time.Second)
		}
	}

	// A console that restarts finds the machine by its worker.
	var found string
	err = poll(3*time.Minute, func() (bool, error) {
		var err error
		found, err = cl.Find(ctx, c, workerID)
		return err == nil && found == machine, err
	})
	if err != nil || found != machine {
		t.Fatalf("Find(%s) = %q, %v; want %q", workerID, found, err, machine)
	}
	step("Find returns it")

	// It keeps running: Exited says no the whole time.
	end := time.Now().Add(hold)
	for time.Now().Before(end) {
		qctx, cancel := context.WithTimeout(ctx, time.Minute)
		exited, code, err := cl.Exited(qctx, c, machine)
		cancel()
		if err != nil {
			t.Fatalf("Exited while running: %v", err)
		}
		if exited {
			t.Fatalf("the machine stopped by itself after %s (exit code %d); see the provider's logs for it", time.Since(t0).Round(time.Second), code)
		}
		time.Sleep(30 * time.Second)
	}
	step("still running after %s", hold)

	sctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	err = cl.Stop(sctx, c, machine)
	cancel()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	removed = true
	step("Stop returned")

	err = poll(10*time.Minute, func() (bool, error) {
		exited, _, err := cl.Exited(ctx, c, machine)
		return exited, err
	})
	if err != nil {
		t.Fatalf("Exited after Stop: %v", err)
	}
	if exited, _, _ := cl.Exited(ctx, c, machine); !exited {
		t.Fatal("the machine still runs 10 minutes after Stop; remove it by hand")
	}
	step("Exited says it stopped")

	// Stopping again, as the launcher may, is not an error.
	if err := cl.Stop(ctx, c, machine); err != nil {
		t.Errorf("a second Stop: %v", err)
	}
	err = poll(3*time.Minute, func() (bool, error) {
		var err error
		found, err = cl.Find(ctx, c, workerID)
		return err == nil && found == "", err
	})
	if err != nil || found != "" {
		t.Fatalf("Find after Stop = %q, %v; want none", found, err)
	}
	step("Find no longer returns it; done in %s", time.Since(t0).Round(time.Second))
}
