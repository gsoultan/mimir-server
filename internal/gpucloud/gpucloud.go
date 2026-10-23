// Package gpucloud rents a GPU for training by the minute. When a
// training run waits that no connected worker can take, the console starts
// a machine on Modal (modal.com) with the GPU chosen under Settings →
// Workers, running this server as a worker that teaches only. The worker
// stops itself once it has had nothing to do for a while, so the GPU is
// paid for only while it trains.
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
	"strings"
	"sync"
	"time"
)

// Config is how the console rents a GPU. It is stored as one setting,
// sealed, since it holds the Modal token and the worker's token and key.
type Config struct {
	Enabled bool `json:"enabled"`
	// TokenID and TokenSecret are a Modal API token. Without them the
	// console uses MODAL_TOKEN_ID and MODAL_TOKEN_SECRET from its own
	// environment, or ~/.modal.toml.
	TokenID     string `json:"token_id,omitempty"`
	TokenSecret string `json:"token_secret,omitempty"`
	// GPU is the Modal GPU type, such as "L4" or "A100-80GB".
	GPU string `json:"gpu"`
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
	DefaultGPU         = "L4"
	DefaultIdleMinutes = 5
	DefaultMaxHours    = 6
	DefaultCapacity    = 1
	// MaxHours is the longest Modal lets a machine run.
	MaxHours = 24
)

// GPUs are the Modal GPU types offered, cheapest first.
var GPUs = []string{"T4", "L4", "A10", "L40S", "A100-40GB", "A100-80GB", "H100", "H200", "B200"}

// WithDefaults fills what c leaves at zero.
func (c Config) WithDefaults() Config {
	if c.GPU == "" {
		c.GPU = DefaultGPU
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
	if (c.TokenID == "") != (c.TokenSecret == "") {
		return errors.New("Give both the Modal token id and its secret, or neither to use the console's MODAL_TOKEN_ID and MODAL_TOKEN_SECRET.")
	}
	if c.TokenID != "" && (!strings.HasPrefix(c.TokenID, "ak-") || !strings.HasPrefix(c.TokenSecret, "as-")) {
		return errors.New("A Modal token id starts with ak- and its secret with as-. Make one under Settings → API tokens on modal.com.")
	}
	gpu, _, _ := strings.Cut(c.GPU, ":")
	if !validGPU(gpu) {
		return fmt.Errorf("Modal has no GPU called %q. Pick one of %s.", c.GPU, strings.Join(GPUs, ", "))
	}
	u, err := url.Parse(c.ConsoleURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" && u.Path != "/" {
		return errors.New("The console address must be the https address the GPU machine can reach this console at, such as https://mimir.example.com.")
	}
	if isLocal(u.Hostname()) {
		return errors.New("The console address must be reachable from the internet: a machine on Modal can't reach localhost or a private address.")
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

func validGPU(g string) bool {
	for _, x := range GPUs {
		if strings.EqualFold(x, g) {
			return true
		}
	}
	// Modal also takes other names for some.
	switch strings.ToUpper(g) {
	case "A10G", "A100", "H100!":
		return true
	}
	return false
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
	// WorkerID tags the machine, so a console that restarts finds it.
	WorkerID string
	GPU      string
	// CPU and MemoryMiB are what the machine gets besides the GPU.
	CPU       float64
	MemoryMiB int
	Timeout   time.Duration
	// Command runs on the machine, with Secrets in its environment.
	Command []string
	Env     map[string]string
	Secrets map[string]string
}

// Cloud starts and stops machines. Modal is the real one; tests use a
// fake.
type Cloud interface {
	// Start starts m and returns its id.
	Start(ctx context.Context, c Config, m Machine) (string, error)
	// Exited reports whether machine id has stopped, and its exit code.
	Exited(ctx context.Context, c Config, id string) (exited bool, code int, err error)
	// Stop stops machine id.
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
	Cloud Cloud
	Log   *slog.Logger
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
	started time.Time
	joined  bool // the machine's worker linked since it started
	looked  bool // looked for a machine a previous run left
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

// NewLauncher returns a Launcher over cloud.
func NewLauncher(cloud Cloud, log *slog.Logger) *Launcher {
	return &Launcher{Cloud: cloud, Log: log, Now: time.Now, wake: make(chan struct{}, 1), cfg: Config{}.WithDefaults(), status: Status{State: StateOff}}
}

// Configure sets the config. Turning it off stops a running machine on
// the next step.
func (l *Launcher) Configure(c Config) {
	l.mu.Lock()
	l.cfg = c.WithDefaults()
	l.retry, l.backoff = time.Time{}, 0
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
	c, id := l.cfg, l.machine
	l.mu.Unlock()
	if id == "" {
		return nil
	}
	if err := l.Cloud.Stop(ctx, c, id); err != nil {
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
	c, id, looked := l.cfg, l.machine, l.looked
	l.mu.Unlock()
	if c.WorkerID == "" {
		return
	}
	if id == "" && !looked && c.Enabled {
		// A console that restarted may have left a machine running.
		found, err := l.Cloud.Find(ctx, c, c.WorkerID)
		if err != nil {
			l.fail(fmt.Errorf("couldn't reach Modal: %w", err))
			return
		}
		l.mu.Lock()
		l.looked = true
		if found != "" && l.machine == "" {
			l.machine, l.started, l.joined, id = found, l.now(), false, found
			l.setLocked(StateStarting, "")
		}
		l.mu.Unlock()
	}
	if id != "" {
		l.watch(ctx, c, id)
		return
	}
	if !c.Enabled || l.Online(c.WorkerID) {
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
	l.Log.InfoContext(ctx, "gpucloud: starting a GPU on Modal", "gpu", c.GPU, "worker_id", c.WorkerID)
	newID, err := l.Cloud.Start(ctx, c, m)
	if err != nil {
		l.fail(fmt.Errorf("Modal didn't start the machine: %w", err))
		return
	}
	l.mu.Lock()
	l.machine, l.started, l.joined = newID, l.now(), false
	l.setLocked(StateStarting, "")
	l.mu.Unlock()
	l.Log.InfoContext(ctx, "gpucloud: started", "machine", newID)
}

// watch follows a running machine: it notes when its worker joins, and
// notices when it stops or never joins.
func (l *Launcher) watch(ctx context.Context, c Config, id string) {
	exited, code, err := l.Cloud.Exited(ctx, c, id)
	if err != nil {
		l.Log.WarnContext(ctx, "gpucloud: ask Modal about the machine", "machine", id, "error", err)
		return
	}
	online := l.Online(c.WorkerID)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.machine != id {
		return
	}
	if online && !l.joined {
		l.joined, l.backoff = true, 0
		l.setLocked(StateRunning, "")
	}
	switch {
	case exited:
		l.machine = ""
		if !l.joined {
			l.failLocked(fmt.Errorf("the machine stopped (exit code %d) before it joined; see its logs under Apps on modal.com", code))
			return
		}
		l.Log.InfoContext(ctx, "gpucloud: the GPU stopped", "machine", id, "code", code)
		l.setLocked(map[bool]string{true: StateAsleep, false: StateOff}[c.Enabled], "")
		if !c.Enabled {
			return
		}
	case !c.Enabled:
		// Turned off while it runs: stop it, and its runs go back in the
		// queue.
		if err := l.Cloud.Stop(ctx, c, id); err != nil {
			l.Log.WarnContext(ctx, "gpucloud: stop the machine", "machine", id, "error", err)
			return
		}
		l.machine = ""
		l.setLocked(StateOff, "")
	case !l.joined && l.now().Sub(l.started) > l.connectTimeout():
		if err := l.Cloud.Stop(ctx, c, id); err != nil {
			l.Log.WarnContext(ctx, "gpucloud: stop the machine", "machine", id, "error", err)
		}
		l.machine = ""
		l.failLocked(fmt.Errorf("the machine didn't join within %s; check that it can reach %s, and its logs under Apps on modal.com", l.connectTimeout(), c.ConsoleURL))
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
	if c.Token == "" || c.Key == "" {
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
	script := strings.Join([]string{
		"set -e",
		"nvidia-smi --query-gpu=name,memory.total --format=csv,noheader || true",
		strings.Join(curl, " "),
		"chmod +x " + programPath,
		fmt.Sprintf("exec %s -role=worker -tasks=train -server %s -data %s -capacity %d -exit-when-idle %dm -name modal-%s",
			programPath, shellQuote(strings.TrimRight(c.ConsoleURL, "/")), dataDir, c.Capacity, c.IdleMinutes, strings.ToLower(c.GPU)),
	}, "\n")
	return Machine{
		WorkerID:  c.WorkerID,
		GPU:       c.GPU,
		// A physical core (two vCPUs) and 4 GB: Modal bills a sandbox's
		// CPU and memory at three times its usual rate, so only what
		// feeding the GPU needs.
		CPU:       1,
		MemoryMiB: 4 << 10,
		Timeout:   time.Duration(c.MaxHours) * time.Hour,
		Command:   []string{"sh", "-c", script},
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

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func parseIP(h string) net.IP { return net.ParseIP(strings.Trim(h, "[]")) }
