package gpucloud

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fake struct {
	starts   int
	startErr error
	exited   map[string]int
	stopped  []string
	found    string
}

func (f *fake) Start(context.Context, Config, Machine) (string, error) {
	if f.startErr != nil {
		return "", f.startErr
	}
	f.starts++
	return "sb-" + strings.Repeat("x", f.starts), nil
}

func (f *fake) Exited(_ context.Context, _ Config, id string) (bool, int, error) {
	code, ok := f.exited[id]
	return ok, code, nil
}

func (f *fake) Stop(_ context.Context, _ Config, id string) error {
	f.stopped = append(f.stopped, id)
	return nil
}

func (f *fake) Find(context.Context, Config, string) (string, error) { return f.found, nil }

func launcher(f *fake, want *bool, online *bool, now *time.Time) *Launcher {
	l := NewLauncher(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.Wanted = func(context.Context, string) (bool, error) { return *want, nil }
	l.Online = func(string) bool { return *online }
	l.Now = func() time.Time { return *now }
	l.ServesProgram = true
	l.Configure(Config{Enabled: true, ConsoleURL: "https://mimir.example.com", WorkerID: "w1", Token: "mimirw_abc", Key: "k"})
	return l
}

func TestLauncher(t *testing.T) {
	ctx := context.Background()
	f := &fake{exited: map[string]int{}}
	want, online, now := false, false, time.Unix(1e9, 0)
	l := launcher(f, &want, &online, &now)

	l.Step(ctx)
	if f.starts != 0 || l.Status().State != StateAsleep {
		t.Fatalf("nothing waiting: %d starts, %v", f.starts, l.Status())
	}
	want = true
	l.Step(ctx)
	l.Step(ctx)
	if f.starts != 1 || l.Status().State != StateStarting || l.Status().Machine != "sb-x" {
		t.Fatalf("a run waits: %d starts, %v", f.starts, l.Status())
	}
	online = true
	l.Step(ctx)
	if l.Status().State != StateRunning {
		t.Fatalf("joined: %v", l.Status())
	}
	// It stops itself when idle; with a run waiting again, another starts.
	f.exited["sb-x"] = 0
	online = false
	l.Step(ctx)
	if l.Status().State != StateAsleep || l.Status().Machine != "" {
		t.Fatalf("stopped: %v", l.Status())
	}
	l.Step(ctx)
	if f.starts != 2 {
		t.Fatalf("starts = %d", f.starts)
	}

	// One that never joins is stopped and counted as failed, and the next
	// try waits.
	now = now.Add(DefaultConnectTimeout + time.Minute)
	l.Step(ctx)
	if st := l.Status(); st.State != StateFailed || !strings.Contains(st.Error, "didn't join") || len(f.stopped) != 1 {
		t.Fatalf("never joined: %v, stopped %v", st, f.stopped)
	}
	l.Step(ctx)
	if f.starts != 2 {
		t.Fatal("tried again at once")
	}
	now = now.Add(2 * time.Minute)
	l.Step(ctx)
	if f.starts != 3 {
		t.Fatalf("starts after waiting = %d", f.starts)
	}

	// One that dies before joining is a failure too.
	f.exited["sb-xxx"] = 1
	l.Step(ctx)
	if st := l.Status(); st.State != StateFailed || !strings.Contains(st.Error, "exit code 1") {
		t.Fatalf("died: %v", st)
	}

	// Modal refusing is a failure, with longer waits each time.
	f.startErr = errors.New("no GPUs")
	now = now.Add(time.Hour)
	l.Step(ctx)
	if st := l.Status(); st.State != StateFailed || !strings.Contains(st.Error, "no GPUs") {
		t.Fatalf("refused: %v", st)
	}
	if l.backoff < 4*time.Minute {
		t.Fatalf("backoff = %v", l.backoff)
	}

	// Turning it off stops a running machine.
	f.startErr = nil
	l.Configure(l.Config())
	now = now.Add(time.Hour)
	l.Step(ctx)
	id := l.Status().Machine
	c := l.Config()
	c.Enabled = false
	l.Configure(c)
	l.Step(ctx)
	if st := l.Status(); st.State != StateOff || f.stopped[len(f.stopped)-1] != id {
		t.Fatalf("turned off: %v, stopped %v", st, f.stopped)
	}
}

func TestLauncherFindsMachine(t *testing.T) {
	f := &fake{exited: map[string]int{}, found: "sb-old"}
	want, online, now := true, false, time.Unix(1e9, 0)
	l := launcher(f, &want, &online, &now)
	l.Step(context.Background())
	if f.starts != 0 || l.Status().Machine != "sb-old" {
		t.Fatalf("a restarted console started another: %d, %v", f.starts, l.Status())
	}
}

func TestMachine(t *testing.T) {
	l := NewLauncher(&fake{}, slog.Default())
	c := Config{Enabled: true, ConsoleURL: "https://mimir.example.com", WorkerID: "w1", Token: "mimirw_abc.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Key: "k"}.WithDefaults()
	if _, err := l.machineFor(c); err == nil {
		t.Fatal("a console that can't hand out its program needs a download address")
	}
	l.ServesProgram = true
	m, err := l.machineFor(c)
	if err != nil {
		t.Fatal(err)
	}
	script := m.Command[2]
	for _, want := range []string{
		"'https://mimir.example.com/api/workers/program'",
		`"Authorization: Bearer $MIMIR_WORKER_SECRET"`,
		"--pinnedpubkey sha256//AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		"-tasks=train -server 'https://mimir.example.com'",
		"-capacity 1 -exit-when-idle 5m",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	if m.Secrets["MIMIR_WORKER_SECRET"] != "mimirw_abc" || m.GPU != "L4" || m.Timeout != 6*time.Hour {
		t.Fatalf("machine = %+v", m)
	}

	// A download address of its own gets no token.
	c.ProgramURL = "https://example.com/mimir-server"
	m, _ = l.machineFor(c)
	if strings.Contains(m.Command[2], "Authorization") || !strings.Contains(m.Command[2], "'https://example.com/mimir-server'") {
		t.Fatalf("own download: %s", m.Command[2])
	}
}

func TestCheck(t *testing.T) {
	ok := Config{Enabled: true, GPU: "A100-80GB", ConsoleURL: "https://mimir.example.com"}.WithDefaults()
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []func(*Config){
		func(c *Config) { c.ConsoleURL = "https://10.0.0.5" },
		func(c *Config) { c.ConsoleURL = "https://mimir.example.com/app" },
		func(c *Config) { c.GPU = "GTX1080" },
		func(c *Config) { c.TokenID, c.TokenSecret = "ak-1", "" },
		func(c *Config) { c.TokenID, c.TokenSecret = "x", "y" },
		func(c *Config) { c.IdleMinutes = 0 },
		func(c *Config) { c.Capacity = 99 },
		func(c *Config) { c.ProgramURL = "http://example.com/x" },
	} {
		c := ok
		bad(&c)
		if c.Check() == nil {
			t.Errorf("%+v passed", c)
		}
	}
	off := Config{GPU: "nonsense"}
	if off.Check() != nil {
		t.Error("a config that is off needs no checking")
	}
}
