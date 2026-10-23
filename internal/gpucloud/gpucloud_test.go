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
	l := NewLauncher(map[string]Cloud{ProviderModal: f, ProviderRunPod: f}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.Wanted = func(context.Context, string) (bool, error) { return *want, nil }
	l.Online = func(string) bool { return *online }
	l.Now = func() time.Time { return *now }
	l.ServesProgram = true
	l.Configure(Config{Enabled: true, ConsoleURL: "https://mimir.example.com", WorkerID: "0b9e6c3a-1111-4222-8333-944445555666", Token: "mimirw_abc", Key: "k"})
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

// A machine that stays when its worker stops, as on RunPod, is removed
// once the worker has been gone a while, or when it runs too long.
func TestLauncherRemovesMachine(t *testing.T) {
	ctx := context.Background()
	f := &fake{exited: map[string]int{}}
	want, online, now := true, false, time.Unix(1e9, 0)
	l := launcher(f, &want, &online, &now)
	c := l.Config()
	c.Provider, c.GPU = ProviderRunPod, "NVIDIA L4"
	l.Configure(c)
	l.Step(ctx)
	online = true
	l.Step(ctx)
	if f.starts != 1 || l.Status().State != StateRunning {
		t.Fatalf("started: %d, %v", f.starts, l.Status())
	}
	want, online = false, false
	l.Step(ctx)
	now = now.Add(GoneGrace / 2)
	l.Step(ctx)
	if len(f.stopped) != 0 {
		t.Fatal("removed before the grace ran out")
	}
	now = now.Add(GoneGrace)
	l.Step(ctx)
	if st := l.Status(); st.State != StateAsleep || st.Machine != "" || len(f.stopped) != 1 {
		t.Fatalf("worker gone: %v, stopped %v", st, f.stopped)
	}

	// Its time runs out while it trains.
	want = true
	l.Step(ctx)
	online = true
	l.Step(ctx)
	now = now.Add(time.Duration(c.WithDefaults().MaxHours)*time.Hour + time.Hour)
	l.Step(ctx)
	if st := l.Status(); st.Machine != "" || len(f.stopped) != 2 {
		t.Fatalf("too long: %v, stopped %v", st, f.stopped)
	}

	// Moving to another provider stops the machine on the old one.
	online = false
	l.Step(ctx)
	c.Provider, c.GPU = ProviderModal, "L4"
	l.Configure(c)
	l.Step(ctx)
	if len(f.stopped) != 3 {
		t.Fatalf("moved: stopped %v", f.stopped)
	}

	// A provider that stopped the machine has it removed too.
	f2 := &fake{exited: map[string]int{}}
	l2 := launcher(f2, &want, &online, &now)
	c.Provider, c.GPU = ProviderRunPod, "NVIDIA L4"
	l2.Configure(c)
	l2.Step(ctx)
	f2.exited["sb-x"] = -1
	l2.Step(ctx)
	if st := l2.Status(); st.State != StateFailed || strings.Contains(st.Error, "exit code") || len(f2.stopped) != 1 {
		t.Fatalf("exited: %v, stopped %v", st, f2.stopped)
	}
}

func TestMachine(t *testing.T) {
	l := NewLauncher(nil, slog.Default())
	c := Config{Enabled: true, ConsoleURL: "https://mimir.example.com", WorkerID: "0B9E6C3A-1111-4222-8333-944445555666", Token: "mimirw_abc.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Key: "k"}.WithDefaults()
	if _, err := l.machineFor(c); err == nil {
		t.Fatal("a console that can't hand out its program needs a download address")
	}
	l.ServesProgram = true
	m, err := l.machineFor(c)
	if err != nil {
		t.Fatal(err)
	}
	script := m.Script()
	for _, want := range []string{
		"'https://mimir.example.com/api/workers/program'",
		`"Authorization: Bearer $MIMIR_WORKER_SECRET"`,
		"--pinnedpubkey sha256//AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		"-tasks=train -server 'https://mimir.example.com'",
		"-capacity 1 -exit-when-idle 5m -name 'modal-l4'",
		"\nexec /usr/local/bin/mimir-server",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	if m.Secrets["MIMIR_WORKER_SECRET"] != "mimirw_abc" || m.GPU != "L4" || m.Timeout != 6*time.Hour || m.Name != "mimir-worker-0b9e6c3a" {
		t.Fatalf("machine = %+v", m)
	}

	// A download address of its own gets no token.
	c.ProgramURL = "https://example.com/mimir-server"
	m, _ = l.machineFor(c)
	if strings.Contains(m.Script(), "Authorization") || !strings.Contains(m.Script(), "'https://example.com/mimir-server'") {
		t.Fatalf("own download: %s", m.Script())
	}

	// A container the provider restarts waits instead of training again.
	cs := m.ContainerScript()
	if !strings.Contains(cs, "apt-get install") || !strings.HasSuffix(cs, "exec sleep infinity") || strings.Contains(cs, "exec /usr/local/bin") {
		t.Fatalf("container script:\n%s", cs)
	}

	// A VM keeps its secrets in a root-only file and powers off after.
	vs := m.VMScript()
	for _, want := range []string{
		"export MIMIR_WORKER_SECRET='mimirw_abc'",
		"chmod 600 /etc/mimir-worker/env",
		"shutdown -h +360",
		"/usr/local/cuda/lib64",
		"shutdown -h now",
		"setsid nohup bash /etc/mimir-worker/run.sh",
	} {
		if !strings.Contains(vs, want) {
			t.Errorf("VM script lacks %q:\n%s", want, vs)
		}
	}
	c.Provider, c.GPU = ProviderAWS, "g4dn.xlarge"
	if m, _ := l.machineFor(c); !strings.Contains(m.Run, "-name 'aws-g4dn.xlarge'") {
		t.Fatalf("worker name: %s", m.Run)
	}
}

func TestCheck(t *testing.T) {
	ok := Config{Enabled: true, GPU: "A100-80GB", ConsoleURL: "https://mimir.example.com"}.WithDefaults()
	if ok.Provider != ProviderModal {
		t.Fatalf("default provider %q", ok.Provider)
	}
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
		func(c *Config) { c.Provider = "azure" },
		func(c *Config) {
			c.Provider, c.GPU = ProviderAWS, "g4dn.xlarge"
			c.TokenID, c.TokenSecret = "ak-1", "as-2"
		},
		func(c *Config) { c.Provider, c.GPU, c.Region = ProviderAWS, "p5.48xlarge", "us-east-1" },
		func(c *Config) { c.Provider, c.GPU, c.Region = ProviderAWS, "g4dn.xlarge", "" },
		func(c *Config) {
			c.Provider, c.GPU, c.Region, c.TokenSecret = ProviderGCP, "nvidia-l4", "us-central1-a", "{}"
		},
		func(c *Config) { c.Provider, c.GPU, c.TokenID, c.TokenSecret = ProviderRunPod, "NVIDIA L4", "", "" },
		func(c *Config) {
			c.Provider, c.GPU, c.Region, c.TokenSecret = ProviderLambda, "gpu_1x_a10", "us-east-1", "key"
		},
		func(c *Config) { c.Provider, c.GPU, c.TokenSecret = ProviderVast, "GTX 1080", "key" },
	} {
		c := ok
		bad(&c)
		if c.Check() == nil {
			t.Errorf("%+v passed", c)
		}
	}
	for _, good := range []Config{
		{Provider: ProviderAWS, GPU: "g6.xlarge", Region: "eu-west-1"},
		{Provider: ProviderAWS, GPU: "g4dn.xlarge", Region: "us-east-1", TokenID: "AKIAEXAMPLE", TokenSecret: "s"},
		{Provider: ProviderGCP, GPU: "nvidia-tesla-t4", Region: "us-central1-a", TokenSecret: `{"project_id":"p","client_email":"a@p.iam.gserviceaccount.com","private_key":"k"}`},
		{Provider: ProviderRunPod, GPU: "NVIDIA L4", TokenSecret: "rpa_x"},
		{Provider: ProviderLambda, GPU: "gpu_1x_a10", Region: "us-east-1", TokenSecret: "secret_x", SSHKey: "me"},
		{Provider: ProviderVast, GPU: "RTX 4090", TokenSecret: "x"},
	} {
		good.Enabled, good.ConsoleURL = true, "https://mimir.example.com"
		if err := good.WithDefaults().Check(); err != nil {
			t.Errorf("%s: %v", good.Provider, err)
		}
	}
	off := Config{GPU: "nonsense"}
	if off.Check() != nil {
		t.Error("a config that is off needs no checking")
	}
}
