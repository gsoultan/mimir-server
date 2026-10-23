// Package memguard keeps a process inside a memory budget, so it slows
// down and sheds work as memory runs short instead of being killed for
// running out.
//
// A Guard knows the budget, from a setting, $GOMEMLIMIT, the container's
// memory limit or the machine's memory, and hands it to the Go runtime as
// its soft memory limit, so the collector works harder before the budget
// is reached rather than after. It then watches how much of the budget is
// in use and reports a Level: OK, High from HighAt of it on, Critical from
// CriticalAt on. Callers ask the Level before taking on memory-hungry work
// (a training run, loading a model, an upload) and are told when it
// changes, to drop what they can spare.
package memguard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level is how tight memory is.
type Level int32

const (
	// OK leaves room for anything.
	OK Level = iota
	// High means memory is getting short: new training waits, and caches
	// give back what they can.
	High
	// Critical means the budget is nearly spent: training here stops and
	// goes back in the queue, and only work already in memory is done.
	Critical
)

func (l Level) String() string {
	switch l {
	case OK:
		return "ok"
	case High:
		return "high"
	case Critical:
		return "critical"
	}
	return "level(" + strconv.Itoa(int(l)) + ")"
}

// ErrLowMemory is the cause of work refused or stopped to keep the
// process within its memory budget. It is temporary: the same work can
// run once memory is free again.
var ErrLowMemory = errors.New("memory is running low")

// Defaults for Config.
const (
	DefaultHighAt     = 0.80
	DefaultCriticalAt = 0.92
	// DefaultShare is the share of the container's or machine's memory
	// taken as the budget when none is set, leaving the rest for memory
	// Go doesn't count: mapped model files, the page cache, other
	// processes.
	DefaultShare = 0.85
	// DefaultEvery is how often memory is measured.
	DefaultEvery = 250 * time.Millisecond
	// hysteresis is how far under a level's threshold use must fall
	// before the level is left, so it doesn't flap at the edge.
	hysteresis = 0.05
)

// Config sets up a Guard. The zero value finds the budget itself.
type Config struct {
	// Limit is the budget in bytes. 0 finds it: $GOMEMLIMIT when set,
	// else DefaultShare of the container's memory limit, else of the
	// machine's memory. Negative turns the guard off: every Level is OK.
	Limit int64
	// HighAt and CriticalAt are the shares of Limit where those levels
	// start; 0 means the defaults.
	HighAt, CriticalAt float64
	// Every is how often Run measures; 0 means DefaultEvery.
	Every time.Duration
	// Log is told when the level changes; nil means slog.Default.
	Log *slog.Logger
	// Name says whose memory this is in logs, such as "console".
	Name string
	// Read measures the bytes in use; nil reads the Go runtime's own
	// accounting. Tests set it.
	Read func() int64
	// KeepRuntimeLimit leaves the Go runtime's soft memory limit as it
	// is instead of setting it to Limit. Tests set it.
	KeepRuntimeLimit bool
}

// Guard watches memory against a budget. A nil *Guard is off: its Level
// is always OK. It is safe for concurrent use.
type Guard struct {
	limit              int64
	highAt, criticalAt float64
	every              time.Duration
	log                *slog.Logger
	name               string
	read               func() int64

	level atomic.Int32
	used  atomic.Int64
	peak  atomic.Int64

	mu       sync.Mutex
	watchers []func(Level)
	freed    time.Time // when FreeOSMemory last ran
}

// New returns a guard for cfg, with the Go runtime's memory limit set to
// its budget unless $GOMEMLIMIT or KeepRuntimeLimit says otherwise. It
// returns nil, a guard that is off, when cfg.Limit is negative or no
// budget can be found.
func New(cfg Config) *Guard {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	limit, from := cfg.Limit, "setting"
	if limit == 0 {
		limit, from = Detect()
	}
	if limit <= 0 {
		if cfg.Limit >= 0 {
			log.Warn("memory: no budget found; set -memory-limit to keep this process within one", "who", cfg.Name)
		}
		return nil
	}
	g := &Guard{limit: limit, highAt: cfg.HighAt, criticalAt: cfg.CriticalAt, every: cfg.Every, log: log, name: cfg.Name, read: cfg.Read}
	if g.highAt <= 0 || g.highAt >= 1 {
		g.highAt = DefaultHighAt
	}
	if g.criticalAt <= g.highAt || g.criticalAt > 1 {
		g.criticalAt = max(DefaultCriticalAt, g.highAt+(1-g.highAt)/2)
	}
	if g.every <= 0 {
		g.every = DefaultEvery
	}
	if g.read == nil {
		g.read = goMemory
	}
	if !cfg.KeepRuntimeLimit && os.Getenv("GOMEMLIMIT") == "" {
		// The collector now works harder as the budget nears, before any
		// level is reached, so garbage never counts toward an out-of-memory
		// kill.
		debug.SetMemoryLimit(limit)
	}
	log.Info("memory: budget set", "who", cfg.Name, "limit_bytes", limit, "limit", Format(limit), "from", from,
		"high_at", Format(int64(float64(limit)*g.highAt)), "critical_at", Format(int64(float64(limit)*g.criticalAt)))
	g.Measure()
	return g
}

// Run measures memory every Config.Every until ctx ends.
func (g *Guard) Run(ctx context.Context) {
	if g == nil {
		return
	}
	t := time.NewTicker(g.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Measure()
		}
	}
}

// Measure reads memory now, updates the level and tells the watchers
// when it changed. It returns the level.
func (g *Guard) Measure() Level {
	if g == nil {
		return OK
	}
	used := g.read()
	g.used.Store(used)
	for {
		p := g.peak.Load()
		if used <= p || g.peak.CompareAndSwap(p, used) {
			break
		}
	}
	old := Level(g.level.Load())
	next := g.levelFor(used, old)
	if next == old || !g.level.CompareAndSwap(int32(old), int32(next)) {
		return Level(g.level.Load())
	}
	attrs := []any{"who", g.name, "from", old.String(), "to", next.String(), "used", Format(used), "limit", Format(g.limit), "used_bytes", used}
	if next > old {
		g.log.Warn("memory: running low; shedding caches and holding back training", attrs...)
	} else {
		g.log.Info("memory: easing", attrs...)
	}
	g.mu.Lock()
	ws := append([]func(Level){}, g.watchers...)
	g.mu.Unlock()
	for _, w := range ws {
		w(next)
	}
	if next == Critical {
		// What the watchers let go is only garbage until collected.
		g.freeOS()
	}
	return next
}

// levelFor is the level at used bytes, coming from old: a level is
// entered at its threshold and left a little under it.
func (g *Guard) levelFor(used int64, old Level) Level {
	share := float64(used) / float64(g.limit)
	critical, high := g.criticalAt, g.highAt
	if old == Critical {
		critical -= hysteresis
	}
	if old >= High {
		high -= hysteresis
	}
	switch {
	case share >= critical:
		return Critical
	case share >= high:
		return High
	}
	return OK
}

// freeOS has the collector run and give memory back to the system now,
// at most every ten seconds: it costs a full collection.
func (g *Guard) freeOS() {
	g.mu.Lock()
	if time.Since(g.freed) < 10*time.Second {
		g.mu.Unlock()
		return
	}
	g.freed = time.Now()
	g.mu.Unlock()
	debug.FreeOSMemory()
}

// Watch calls f with the new level whenever it changes, from the
// goroutine that measured. f must not block.
func (g *Guard) Watch(f func(Level)) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.watchers = append(g.watchers, f)
	g.mu.Unlock()
}

// Level returns the level last measured.
func (g *Guard) Level() Level {
	if g == nil {
		return OK
	}
	return Level(g.level.Load())
}

// Limit returns the budget in bytes, 0 when the guard is off.
func (g *Guard) Limit() int64 {
	if g == nil {
		return 0
	}
	return g.limit
}

// Used returns the bytes in use when last measured.
func (g *Guard) Used() int64 {
	if g == nil {
		return 0
	}
	return g.used.Load()
}

// Peak returns the most bytes measured in use so far.
func (g *Guard) Peak() int64 {
	if g == nil {
		return 0
	}
	return g.peak.Load()
}

// Free returns the bytes left before the High level, 0 when it is
// reached; math.MaxInt64 when the guard is off.
func (g *Guard) Free() int64 {
	if g == nil {
		return math.MaxInt64
	}
	return max(0, int64(float64(g.limit)*g.highAt)-g.used.Load())
}

// Admit reports whether work that needs about need more bytes can start
// now: the level is OK and need fits before High. need may be 0 when it
// isn't known.
func (g *Guard) Admit(need int64) bool {
	if g == nil {
		return true
	}
	return g.Level() == OK && need <= g.Free()
}

// Check returns an error wrapping ErrLowMemory when the level is at least
// l, for work that shouldn't start then.
func (g *Guard) Check(l Level) error {
	if got := g.Level(); got >= l {
		return fmt.Errorf("%w (%s of %s in use)", ErrLowMemory, Format(g.Used()), Format(g.Limit()))
	}
	return nil
}

// InUse returns the memory the process holds for live data now, as a
// Guard counts it.
func InUse() int64 { return goMemory() }

// goMemory is the memory the process holds for live data: the heap
// still reachable at the last collection, plus everything else the Go
// runtime holds from the system and hasn't given back (goroutine stacks,
// its own structures, heap set aside but not yet filled). Garbage not yet
// collected doesn't count: the runtime's memory limit has the collector
// take it back before it matters, so counting it would shed caches and
// hold back training for memory that is about to be free.
func goMemory() int64 {
	s := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/memory/classes/heap/free:bytes"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/gc/heap/live:bytes"},
	}
	metrics.Read(s)
	var v [5]uint64
	for i := range s {
		if s[i].Value.Kind() == metrics.KindUint64 {
			v[i] = s[i].Value.Uint64()
		}
	}
	total, released, free, objects, live := v[0], v[1], v[2], v[3], v[4]
	other := total - min(total, released+free+objects)
	return int64(min(live, objects) + other)
}

// Detect finds a memory budget for this process: $GOMEMLIMIT when it is
// set, else DefaultShare of the container's memory limit, else of the
// machine's memory. It says where the number came from, and returns 0
// when it finds none.
func Detect() (int64, string) {
	if os.Getenv("GOMEMLIMIT") != "" {
		if l := debug.SetMemoryLimit(-1); l > 0 && l < math.MaxInt64 {
			return l, "$GOMEMLIMIT"
		}
	}
	if c := cgroupLimit(); c > 0 {
		return int64(float64(c) * DefaultShare), "container limit"
	}
	if m := machineMemory(); m > 0 {
		return int64(float64(m) * DefaultShare), "machine memory"
	}
	return 0, ""
}

// cgroupRoot is where cgroup files are read from; tests move it.
var cgroupRoot = "/sys/fs/cgroup"

// cgroupLimit reads the memory limit of this process's cgroup, v2 or v1,
// or 0 without one.
func cgroupLimit() int64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	machine := machineMemory()
	for _, f := range []string{"/memory.max", "/memory/memory.limit_in_bytes"} {
		b, err := os.ReadFile(cgroupRoot + f)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "max" {
			return 0
		}
		n, err := strconv.ParseInt(s, 10, 64)
		// v1 says "no limit" with a number near the largest one.
		if err != nil || n <= 0 || (machine > 0 && n >= machine) {
			return 0
		}
		return n
	}
	return 0
}

// meminfo is the file the machine's memory is read from; tests move it.
var meminfo = "/proc/meminfo"

// machineMemory returns the machine's memory in bytes, or 0 where it
// can't be read.
func machineMemory() int64 {
	b, err := os.ReadFile(meminfo)
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}

// Parse reads a budget as typed on a command line: a number of bytes with
// an optional unit (512MiB, 4GB, 6g), a share of the container's or
// machine's memory (75%), or "off". Empty means 0, find it.
func Parse(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "auto":
		return 0, nil
	case "off", "none", "-1":
		return -1, nil
	}
	if pct, ok := strings.CutSuffix(s, "%"); ok {
		p, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil || p <= 0 || p > 100 {
			return 0, fmt.Errorf("memory limit %q: want a share between 0%% and 100%%", s)
		}
		total := cgroupLimit()
		if total <= 0 {
			total = machineMemory()
		}
		if total <= 0 {
			return 0, fmt.Errorf("memory limit %q: this machine's memory can't be read; give it in bytes, such as 4GiB", s)
		}
		return int64(float64(total) * p / 100), nil
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
		{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12},
		{"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30}, {"t", 1 << 40},
		{"b", 1},
	}
	mult := 1.0
	for _, u := range units {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = strings.TrimSpace(rest), u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return 0, fmt.Errorf("memory limit: want a size such as 4GiB or 512MiB, a share such as 75%%, or off")
	}
	n := v * mult
	if n < 64<<20 {
		return 0, fmt.Errorf("memory limit %s is under 64 MiB, too little to run in", Format(int64(n)))
	}
	return int64(n), nil
}

// Format writes n bytes for people, such as 1.5 GiB.
func Format(n int64) string {
	const unit = 1 << 10
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "iB"
}
