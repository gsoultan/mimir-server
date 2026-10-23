// Package gpucloud rents a GPU for training by the minute. When a
// training run waits that no connected worker can take, the console starts
// a machine with the GPU chosen under Settings → Workers, on the provider
// chosen there (Modal, AWS EC2, Google Cloud, RunPod, Lambda Cloud or
// Vast.ai), running this server as a worker that teaches only. The worker
// stops once it has had nothing to do for a while, and the console removes
// the machine, so the GPU is paid for only while it trains.
//
// The rented machine joins as an ordinary worker, with a token and key
// the console made for it and keeps sealed with its other secrets, so it
// is the same worker each time it starts.
package gpucloud

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Config is how the console rents a GPU. It is stored as one setting,
// sealed, since it holds the provider's credentials and the worker's token
// and key.
type Config struct {
	Enabled bool `json:"enabled"`
	// Provider is where the GPU is rented: one of Providers.
	Provider string `json:"provider"`
	// TokenID and TokenSecret are the provider's credentials:
	//   - Modal: an API token. Without it the console uses MODAL_TOKEN_ID
	//     and MODAL_TOKEN_SECRET from its environment, or ~/.modal.toml.
	//   - AWS: an access key. Without it the console uses its own AWS
	//     credentials (environment, ~/.aws or an instance role).
	//   - Google Cloud: TokenSecret is a service account's JSON key.
	//   - RunPod, Lambda Cloud, Vast.ai: TokenSecret is an API key.
	TokenID     string `json:"token_id,omitempty"`
	TokenSecret string `json:"token_secret,omitempty"`
	// GPU is what the provider calls the machine: a GPU type ("L4"), an
	// instance type ("g6.xlarge") or a GPU name ("RTX 4090").
	GPU string `json:"gpu"`
	// Region is the AWS or Lambda region, or Google Cloud zone.
	Region string `json:"region,omitempty"`
	// SSHKey names the SSH key Lambda Cloud requires on every machine.
	SSHKey string `json:"ssh_key,omitempty"`
	// ConsoleURL is the https address the machine reaches the console at.
	ConsoleURL string `json:"console_url"`
	// IdleMinutes is how long the worker waits for more work before it
	// stops; MaxHours the longest one machine runs.
	IdleMinutes int `json:"idle_minutes"`
	MaxHours    int `json:"max_hours"`
	// Capacity is how many runs the worker trains at once.
	Capacity int `json:"capacity"`
	// ProgramURL is where the machine downloads mimir-server for
	// linux/amd64 from; empty takes it from the console.
	ProgramURL string `json:"program_url,omitempty"`
	// WorkerID, Token and Key are the worker the machine joins as: its
	// record, its join token and its private key (worker.EncodeKey).
	WorkerID string `json:"worker_id,omitempty"`
	Token    string `json:"token,omitempty"`
	Key      string `json:"key,omitempty"`
}

// Defaults for a Config left at zero.
const (
	DefaultProvider    = ProviderModal
	DefaultIdleMinutes = 5
	DefaultMaxHours    = 6
	DefaultCapacity    = 1
	// MaxHours is the longest a machine may run; Modal allows no more.
	MaxHours = 24
)

// WithDefaults fills what c leaves at zero.
func (c Config) WithDefaults() Config {
	if c.Provider == "" {
		c.Provider = DefaultProvider
	}
	if p, ok := ProviderByID(c.Provider); ok {
		if c.GPU == "" {
			c.GPU = p.DefaultGPU
		}
		if c.Region == "" {
			c.Region = p.DefaultRegion
		}
	}
	if c.IdleMinutes <= 0 {
		c.IdleMinutes = DefaultIdleMinutes
	}
	if c.MaxHours <= 0 {
		c.MaxHours = DefaultMaxHours
	}
	if c.Capacity <= 0 {
		c.Capacity = DefaultCapacity
	}
	return c
}

// Check reports what is wrong with a Config that is turned on, in words
// for the person setting it up.
func (c Config) Check() error {
	if !c.Enabled {
		return nil
	}
	p, ok := ProviderByID(c.Provider)
	if !ok {
		return fmt.Errorf("There is no GPU provider called %q.", c.Provider)
	}
	if err := c.checkProvider(p); err != nil {
		return err
	}
	u, err := url.Parse(c.ConsoleURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" && u.Path != "/" {
		return errors.New("The console address must be the https address the GPU machine can reach this console at, such as https://mimir.example.com.")
	}
	if isLocal(u.Hostname()) {
		return fmt.Errorf("The console address must be reachable from the internet: a machine on %s can't reach localhost or a private address.", p.Name)
	}
	if c.IdleMinutes < 1 || c.IdleMinutes > 120 {
		return errors.New("Wait between 1 and 120 minutes for more work before stopping.")
	}
	if c.MaxHours < 1 || c.MaxHours > MaxHours {
		return fmt.Errorf("A machine can run between 1 and %d hours.", MaxHours)
	}
	if c.Capacity < 1 || c.Capacity > 16 {
		return errors.New("Train between 1 and 16 runs at once.")
	}
	if c.ProgramURL != "" {
		if u, err := url.Parse(c.ProgramURL); err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("The download address for mimir-server must be an https address.")
		}
	}
	return nil
}

func isLocal(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return true
	}
	if ip := parseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	return false
}

// Machine is what the console asks a cloud to start.
type Machine struct {
	// WorkerID tags the machine, so a console that restarts finds it;
	// Name is what the provider shows for it.
	WorkerID string
	Name     string
	GPU      string
	Region   string
	// CPU and MemoryMiB are what the machine gets besides the GPU, where
	// the provider lets them be picked.
	CPU       float64
	MemoryMiB int
	Timeout   time.Duration
	// Setup fetches the program; Run then runs the worker until it has
	// been idle long enough. Both are sh, with Env and Secrets in the
	// environment.
	Setup   string
	Run     string
	Env     map[string]string
	Secrets map[string]string
}

// Script is Setup then Run, ending when the worker does: for a container
// that stops when its command ends.
func (m Machine) Script() string {
	return "set -e\n" + m.Setup + "\nexec " + m.Run
}

// containerImage is NVIDIA's CUDA runtime, which carries the libraries
// mimir's GPU backend loads, for providers that run a container. CUDA
// 12.4 runs on the drivers most hosts have.
const containerImage = "nvidia/cuda:12.4.1-runtime-ubuntu22.04"

// ContainerScript is for a container that the provider restarts when its
// command ends: it gets curl, runs the worker, then waits for the console
// to remove the machine, rather than starting the worker again.
func (m Machine) ContainerScript() string {
	return "set -e\n" +
		"command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq --no-install-recommends curl ca-certificates >/dev/null; }\n" +
		m.Setup + "\nset +e\n" + m.Run + "\necho 'mimir-worker: stopped; waiting for the console to remove this machine'\nexec sleep infinity"
}

// VMScript is the boot script for a virtual machine with NVIDIA's driver
// and CUDA installed: it runs the worker as root in the background, then
// powers the machine off once the worker stops or MaxHours pass, in case
// the console can't remove it.
func (m Machine) VMScript() string {
	var env strings.Builder
	for _, kv := range []map[string]string{m.Env, m.Secrets} {
		for _, k := range sortedKeys(kv) {
			fmt.Fprintf(&env, "export %s=%s\n", k, shellQuote(kv[k]))
		}
	}
	run := "set -e\n" +
		"for i in $(seq 120); do nvidia-smi >/dev/null 2>&1 && break; sleep 5; done\n" +
		"export LD_LIBRARY_PATH=/usr/local/cuda/lib64${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}\n" +
		". /etc/mimir-worker/env\n" +
		m.Setup + "\nset +e\n" + m.Run + "\nshutdown -h now\n"
	return "#!/bin/bash\n" +
		"mkdir -p /etc/mimir-worker && chmod 700 /etc/mimir-worker\n" +
		"cat >/etc/mimir-worker/env <<'MIMIR_ENV'\n" + env.String() + "MIMIR_ENV\n" +
		"cat >/etc/mimir-worker/run.sh <<'MIMIR_RUN'\n" + run + "MIMIR_RUN\n" +
		"chmod 600 /etc/mimir-worker/env\n" +
		fmt.Sprintf("shutdown -h +%d || true\n", int(m.Timeout.Minutes())) +
		"setsid nohup bash /etc/mimir-worker/run.sh >/var/log/mimir-worker.log 2>&1 </dev/null &\n"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Cloud starts and stops machines on one provider; tests use a fake.
type Cloud interface {
	// Start starts m and returns its id.
	Start(ctx context.Context, c Config, m Machine) (string, error)
	// Exited reports whether machine id has stopped or is gone, and its
	// exit code, or -1 where the provider doesn't say.
	Exited(ctx context.Context, c Config, id string) (exited bool, code int, err error)
	// Stop stops machine id and removes it, so it costs nothing more.
	Stop(ctx context.Context, c Config, id string) error
	// Find returns a running machine started for workerID, or "".
	Find(ctx context.Context, c Config, workerID string) (string, error)
}

// States of the rented GPU.
const (
	StateOff      = "off"
	StateAsleep   = "asleep"
	StateStarting = "starting"
	StateRunning  = "running"
	StateFailed   = "failed"
)

// Status is what the rented GPU is doing.
type Status struct {
	State   string
	Since   time.Time
	Error   string
	Machine string // the machine's id while one runs
}

// Launcher starts a machine when training waits for one, and keeps track
// of it.
type Launcher struct {
	// Clouds are the providers, by Provider.ID.
	Clouds map[string]Cloud
	Log    *slog.Logger
	// Wanted reports whether a training run waits that the worker could
	// take. Online reports whether the worker has a link open.
	Wanted func(ctx context.Context, workerID string) (bool, error)
	Online func(workerID string) bool
	// ServesProgram says the console hands its own program out at
	// ProgramPath, which the machine downloads when Config.ProgramURL is
	// empty. It can when it runs on linux/amd64 itself.
	ServesProgram bool
	// ConnectTimeout is how long a started machine has to join before it
	// is stopped as failed. Zero means DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// Now tells the time; tests move it.
	Now func() time.Time

	mu      sync.Mutex
	cfg     Config
	status  Status
	machine string
	mcfg    Config // the config the machine was started with
	started time.Time
	joined  bool      // the machine's worker linked since it started
	gone    time.Time // when the joined worker was last seen gone
	looked  bool      // looked for a machine a previous run left
	retry   time.Time
	backoff time.Duration
	wake    chan struct{}
}

// ProgramPath is where a console hands its own program to its workers,
// so a rented GPU runs the console's version.
const ProgramPath = "/api/workers/program"

// DefaultConnectTimeout is how long a machine has to download the program
// and join; pulling the CUDA image the first time takes a few minutes.
const DefaultConnectTimeout = 15 * time.Minute

// GoneGrace is how long a worker that joined may stay away while its
// machine still runs before the console removes the machine: the worker
// stopped when idle, or lost its way back.
const GoneGrace = 3 * time.Minute

// NewLauncher returns a Launcher over clouds, by provider.
func NewLauncher(clouds map[string]Cloud, log *slog.Logger) *Launcher {
	return &Launcher{Clouds: clouds, Log: log, Now: time.Now, wake: make(chan struct{}, 1), cfg: Config{}.WithDefaults(), status: Status{State: StateOff}}
}

// cloud returns the Cloud for c's provider.
func (l *Launcher) cloud(c Config) (Cloud, Provider, error) {
	p, ok := ProviderByID(c.Provider)
	if cl := l.Clouds[c.Provider]; ok && cl != nil {
		return cl, p, nil
	}
	return nil, p, fmt.Errorf("this console can't rent from %q", c.Provider)
}

// Configure sets the config. Turning it off stops a running machine on
// the next step.
func (l *Launcher) Configure(c Config) {
	l.mu.Lock()
	l.cfg = c.WithDefaults()
	l.retry, l.backoff, l.looked = time.Time{}, 0, false
	if !c.Enabled {
		if l.machine == "" {
			l.setLocked(StateOff, "")
		}
	} else if l.status.State == StateOff || l.status.State == StateFailed {
		l.setLocked(StateAsleep, "")
	}
	l.mu.Unlock()
	l.Kick()
}

// Config returns the config.
func (l *Launcher) Config() Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cfg
}

// Status returns what the rented GPU is doing.
func (l *Launcher) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Kick asks the launcher to look now, such as when a run is queued.
func (l *Launcher) Kick() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Run steps every few seconds until ctx ends. A machine still running
// then is left to stop itself when idle.
func (l *Launcher) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		l.Step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-t.C:
		}
	}
}

// Stop stops the machine now, if one runs.
func (l *Launcher) Stop(ctx context.Context) error {
	l.mu.Lock()
	c, mc, id := l.cfg, l.mcfg, l.machine
	l.mu.Unlock()
	if id == "" {
		return nil
	}
	cl, _, err := l.cloud(mc)
	if err != nil {
		return err
	}
	if err := cl.Stop(ctx, mc, id); err != nil {
		return err
	}
	l.mu.Lock()
	if l.machine == id {
		l.machine = ""
		l.setLocked(map[bool]string{true: StateAsleep, false: StateOff}[c.Enabled], "")
		// Don't start it again at once for the runs it leaves behind.
		l.retry = l.now().Add(time.Minute)
	}
	l.mu.Unlock()
	return nil
}

// Step looks once: at the machine running, or at whether one is wanted.
func (l *Launcher) Step(ctx context.Context) {
	l.mu.Lock()
	c, mc, id, looked := l.cfg, l.mcfg, l.machine, l.looked
	l.mu.Unlock()
	if c.WorkerID == "" {
		return
	}
	if id != "" {
		l.watch(ctx, c, mc, id)
		return
	}
	if !c.Enabled {
		return
	}
	cl, p, err := l.cloud(c)
	if err != nil {
		l.fail(err)
		return
	}
	if !looked {
		// A console that restarted may have left a machine running.
		found, err := cl.Find(ctx, c, c.WorkerID)
		if err != nil {
			l.fail(fmt.Errorf("couldn't reach %s: %w", p.Name, err))
			return
		}
		l.mu.Lock()
		l.looked = true
		if found != "" && l.machine == "" {
			l.machine, l.mcfg, l.started, l.joined, l.gone = found, c, l.now(), false, time.Time{}
			l.setLocked(StateStarting, "")
		}
		l.mu.Unlock()
		if found != "" {
			return
		}
	}
	if l.Online(c.WorkerID) {
		return
	}
	l.mu.Lock()
	wait := l.now().Before(l.retry)
	l.mu.Unlock()
	if wait {
		return
	}
	want, err := l.Wanted(ctx, c.WorkerID)
	if err != nil {
		l.Log.WarnContext(ctx, "gpucloud: look at the queue", "error", err)
		return
	}
	if !want {
		l.mu.Lock()
		if l.status.State != StateFailed {
			l.setLocked(StateAsleep, "")
		}
		l.mu.Unlock()
		return
	}
	m, err := l.machineFor(c)
	if err != nil {
		l.fail(err)
		return
	}
	l.mu.Lock()
	l.setLocked(StateStarting, "")
	l.mu.Unlock()
	l.Log.InfoContext(ctx, "gpucloud: starting a GPU", "provider", p.ID, "gpu", c.GPU, "worker_id", c.WorkerID)
	newID, err := cl.Start(ctx, c, m)
	if err != nil {
		l.fail(fmt.Errorf("%s didn't start the machine: %w", p.Name, err))
		return
	}
	l.mu.Lock()
	l.machine, l.mcfg, l.started, l.joined, l.gone = newID, c, l.now(), false, time.Time{}
	l.setLocked(StateStarting, "")
	l.mu.Unlock()
	l.Log.InfoContext(ctx, "gpucloud: started", "machine", newID)
}

// watch follows a running machine, started with mc: it notes when its
// worker joins, notices when it stops or never joins, and removes it once
// it is no longer wanted or has run too long.
func (l *Launcher) watch(ctx context.Context, c, mc Config, id string) {
	cl, p, err := l.cloud(mc)
	if err != nil {
		l.fail(err)
		return
	}
	exited, code, err := cl.Exited(ctx, mc, id)
	if err != nil {
		l.Log.WarnContext(ctx, "gpucloud: ask about the machine", "provider", p.ID, "machine", id, "error", err)
		return
	}
	online := l.Online(mc.WorkerID)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.machine != id {
		return
	}
	if online {
		l.gone = time.Time{}
		if !l.joined {
			l.joined, l.backoff = true, 0
			l.setLocked(StateRunning, "")
		}
	} else if l.joined && l.gone.IsZero() {
		l.gone = now
	}
	// stop removes the machine; it reports whether it did.
	stop := func() bool {
		if err := cl.Stop(ctx, mc, id); err != nil {
			l.Log.WarnContext(ctx, "gpucloud: stop the machine", "provider", p.ID, "machine", id, "error", err)
			return false
		}
		l.machine = ""
		return true
	}
	asleep := map[bool]string{true: StateAsleep, false: StateOff}[c.Enabled]
	switch {
	case exited:
		// Remove what is left of it, such as its disk.
		if !p.OwnTimeout {
			if err := cl.Stop(ctx, mc, id); err != nil {
				l.Log.WarnContext(ctx, "gpucloud: remove the stopped machine", "provider", p.ID, "machine", id, "error", err)
			}
		}
		l.machine = ""
		if !l.joined {
			how := "stopped"
			if code >= 0 {
				how = fmt.Sprintf("stopped (exit code %d)", code)
			}
			l.failLocked(fmt.Errorf("the machine %s before it joined; %s", how, p.Logs))
			return
		}
		l.Log.InfoContext(ctx, "gpucloud: the GPU stopped", "provider", p.ID, "machine", id, "code", code)
		l.setLocked(asleep, "")
	case !c.Enabled || c.Provider != mc.Provider:
		// Turned off or moved while it runs: stop it, and its runs go back
		// in the queue.
		if stop() {
			l.setLocked(asleep, "")
		}
	case !l.joined && now.Sub(l.started) > l.connectTimeout():
		stop()
		l.machine = ""
		l.failLocked(fmt.Errorf("the machine didn't join within %s; check that it can reach %s, and %s", l.connectTimeout(), mc.ConsoleURL, p.Logs))
	case l.joined && !l.gone.IsZero() && now.Sub(l.gone) > GoneGrace:
		// The worker stopped when idle, and the machine stays.
		l.Log.InfoContext(ctx, "gpucloud: the worker left; removing its machine", "provider", p.ID, "machine", id)
		if stop() {
			l.setLocked(asleep, "")
		}
	case now.Sub(l.started) > time.Duration(mc.MaxHours)*time.Hour+10*time.Minute:
		// Past its limit: runs on it resume on a new machine.
		l.Log.InfoContext(ctx, "gpucloud: the machine reached its time limit", "provider", p.ID, "machine", id)
		if stop() {
			l.setLocked(asleep, "")
		}
	}
}

func (l *Launcher) connectTimeout() time.Duration {
	if l.ConnectTimeout > 0 {
		return l.ConnectTimeout
	}
	return DefaultConnectTimeout
}

func (l *Launcher) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failLocked(err)
}

// failLocked records err and waits before trying again, longer each
// time, so a mistake in the setup doesn't start machine after machine.
func (l *Launcher) failLocked(err error) {
	l.Log.Warn("gpucloud: " + err.Error())
	l.backoff = min(max(2*l.backoff, time.Minute), 30*time.Minute)
	l.retry = l.now().Add(l.backoff)
	l.setLocked(StateFailed, err.Error())
}

func (l *Launcher) setLocked(state, errText string) {
	if l.status.State != state || l.status.Error != errText {
		l.status = Status{State: state, Since: l.now(), Error: errText}
	}
	l.status.Machine = l.machine
}

func (l *Launcher) now() time.Time {
	if l.Now == nil {
		return time.Now()
	}
	return l.Now()
}

// Paths on the machine.
const (
	programPath = "/usr/local/bin/mimir-server"
	dataDir     = "/var/lib/mimir"
)

// machineFor describes the machine to start for c.
func (l *Launcher) machineFor(c Config) (Machine, error) {
	program, auth := c.ProgramURL, false
	if program == "" {
		if !l.ServesProgram {
			return Machine{}, errors.New("this console can't hand the GPU machine its program, since it doesn't run on linux/amd64; give a download address for mimir-server for linux/amd64")
		}
		program, auth = strings.TrimRight(c.ConsoleURL, "/")+ProgramPath, true
	}
	if c.Token == "" || c.Key == "" || len(c.WorkerID) < 8 {
		return Machine{}, errors.New("the GPU's worker has no token; turn renting a GPU off and on again")
	}
	secret, pin, _ := strings.Cut(c.Token, ".")
	curl := []string{"curl", "-fsSL", "--retry", "5", "--retry-all-errors", "-o", programPath}
	if auth {
		// The console only hands its program to its own workers.
		curl = append(curl, "-H", `"Authorization: Bearer $MIMIR_WORKER_SECRET"`)
		if pin != "" {
			// A console with its own certificate: accept only its key,
			// as the worker does.
			raw, err := base64.RawURLEncoding.DecodeString(pin)
			if err != nil {
				return Machine{}, errors.New("the GPU's worker token is damaged; turn renting a GPU off and on again")
			}
			curl = append(curl, "-k", "--pinnedpubkey", "sha256//"+base64.StdEncoding.EncodeToString(raw))
		}
	}
	curl = append(curl, shellQuote(program))
	setup := strings.Join([]string{
		"nvidia-smi --query-gpu=name,memory.total --format=csv,noheader || true",
		strings.Join(curl, " "),
		"chmod +x " + programPath,
	}, "\n")
	run := fmt.Sprintf("%s -role=worker -tasks=train -server %s -data %s -capacity %d -exit-when-idle %dm -name %s",
		programPath, shellQuote(strings.TrimRight(c.ConsoleURL, "/")), dataDir, c.Capacity, c.IdleMinutes, shellQuote(workerName(c)))
	return Machine{
		WorkerID: c.WorkerID,
		Name:     machineName(c.WorkerID),
		GPU:      c.GPU,
		Region:   c.Region,
		// A physical core (two vCPUs) and 4 GB: Modal bills a sandbox's
		// CPU and memory at three times its usual rate, so only what
		// feeding the GPU needs.
		CPU:       1,
		MemoryMiB: 4 << 10,
		Timeout:   time.Duration(c.MaxHours) * time.Hour,
		Setup:     setup,
		Run:       run,
		Env: map[string]string{
			"MIMIR_DEVICE":    "gpu",
			"MIMIR_CACHE_DIR": dataDir + "/cache",
		},
		Secrets: map[string]string{
			"MIMIR_WORKER_TOKEN":  c.Token,
			"MIMIR_WORKER_SECRET": secret,
			"MIMIR_WORKER_KEY":    c.Key,
		},
	}, nil
}

// machineName is what the provider shows for the machine of workerID; the
// providers without tags find it by this name.
func machineName(workerID string) string {
	return "mimir-worker-" + strings.ToLower(workerID[:min(8, len(workerID))])
}

// workerName is the name the machine's worker gives itself, such as
// modal-l4 or aws-g6.xlarge.
func workerName(c Config) string {
	g := strings.Map(func(r rune) rune {
		if r == ' ' || r == '_' {
			return '-'
		}
		return r
	}, strings.ToLower(c.GPU))
	return c.Provider + "-" + g
}

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func parseIP(h string) net.IP { return net.ParseIP(strings.Trim(h, "[]")) }
