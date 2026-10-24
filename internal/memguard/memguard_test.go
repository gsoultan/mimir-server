package memguard

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fake is memory in use that a test sets.
type fake struct{ n atomic.Int64 }

func (f *fake) read() int64 { return f.n.Load() }

// The level climbs at each threshold, falls a little under it, and every
// change reaches the watchers once.
func TestLevels(t *testing.T) {
	var mem fake
	g := New(Config{Limit: 1000, Read: mem.read, Log: quiet, KeepRuntimeLimit: true})
	var seen []Level
	g.Watch(func(l Level) { seen = append(seen, l) })
	steps := []struct {
		used int64
		want Level
	}{
		{100, OK},
		{799, OK},
		{800, High},     // HighAt
		{760, High},     // under it, but not by the hysteresis yet
		{749, OK},       // now it is
		{920, Critical}, // CriticalAt
		{880, Critical},
		{869, High},
		{10, OK},
	}
	for _, s := range steps {
		mem.n.Store(s.used)
		if got := g.Measure(); got != s.want {
			t.Fatalf("at %d bytes: level %v, want %v", s.used, got, s.want)
		}
	}
	if want := []Level{High, OK, Critical, High, OK}; !slices.Equal(seen, want) {
		t.Fatalf("watchers saw %v, want %v", seen, want)
	}
	if g.Peak() != 920 {
		t.Fatalf("Peak() = %d, want 920", g.Peak())
	}
}

func TestAdmitAndCheck(t *testing.T) {
	var mem fake
	g := New(Config{Limit: 1000, Read: mem.read, Log: quiet, KeepRuntimeLimit: true})
	mem.n.Store(500)
	g.Measure()
	if !g.Admit(0) || !g.Admit(300) || g.Admit(301) {
		t.Fatalf("at 500 of 1000: Admit(0), Admit(300), Admit(301) = %v %v %v, want true true false", g.Admit(0), g.Admit(300), g.Admit(301))
	}
	if err := g.Check(High); err != nil {
		t.Fatalf("Check(High) at OK = %v", err)
	}
	mem.n.Store(950)
	g.Measure()
	if g.Admit(0) {
		t.Fatal("Admit at critical")
	}
	if err := g.Check(Critical); !errors.Is(err, ErrLowMemory) {
		t.Fatalf("Check(Critical) at critical = %v, want ErrLowMemory", err)
	}

	// A nil guard is off: everything is allowed.
	var off *Guard
	if off.Level() != OK || !off.Admit(1<<60) || off.Check(Critical) != nil || off.Limit() != 0 {
		t.Fatal("a nil guard should be off")
	}
	off.Watch(func(Level) {})
	off.Measure()
	if New(Config{Limit: -1, Log: quiet}) != nil {
		t.Fatal("a negative limit should turn the guard off")
	}
}

// Real memory: holding more than the budget makes the level critical,
// and letting it go brings it back to OK.
func TestRealMemory(t *testing.T) {
	runtime.GC()
	base := goMemory()
	g := New(Config{Limit: base + 128<<20, Log: quiet, KeepRuntimeLimit: true})
	if g.Measure() != OK {
		t.Fatalf("at the start: %v (%s of %s)", g.Level(), Format(g.Used()), Format(g.Limit()))
	}
	ballast := make([]byte, 192<<20)
	for i := range ballast {
		ballast[i] = byte(i)
	}
	runtime.GC()
	if g.Measure() != Critical {
		t.Fatalf("holding 192 MiB over a budget 128 MiB over the start: %v (%s of %s)", g.Level(), Format(g.Used()), Format(g.Limit()))
	}
	runtime.KeepAlive(ballast)
	ballast = nil
	runtime.GC()
	if g.Measure() != OK {
		t.Fatalf("after letting go: %v (%s of %s)", g.Level(), Format(g.Used()), Format(g.Limit()))
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]int64{
		"":       0,
		"auto":   0,
		"off":    -1,
		"4GiB":   4 << 30,
		"4g":     4 << 30,
		"512MiB": 512 << 20,
		"2GB":    2e9,
		"1.5GiB": 3 << 29,
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"lots", "-3GiB", "1MiB", "150%", "0%"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) took it", in)
		}
	}
}

// The budget comes from the container's limit when there is one, else
// from the machine's memory, and a share is of whichever it is.
func TestDetect(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroups are Linux's")
	}
	t.Setenv("GOMEMLIMIT", "")
	os.Unsetenv("GOMEMLIMIT")
	dir := t.TempDir()
	oldRoot, oldInfo := cgroupRoot, meminfo
	t.Cleanup(func() { cgroupRoot, meminfo = oldRoot, oldInfo })
	cgroupRoot, meminfo = dir, filepath.Join(dir, "meminfo")
	os.WriteFile(meminfo, []byte("MemTotal:       16777216 kB\nMemFree: 1 kB\n"), 0o600)

	limit, from := Detect()
	if from != "machine memory" || limit != share(16<<30) {
		t.Fatalf("no cgroup: %d from %q", limit, from)
	}
	os.WriteFile(filepath.Join(dir, "memory.max"), []byte("max\n"), 0o600)
	if _, from := Detect(); from != "machine memory" {
		t.Fatalf("cgroup without a limit: from %q", from)
	}
	os.WriteFile(filepath.Join(dir, "memory.max"), []byte("4294967296\n"), 0o600)
	limit, from = Detect()
	if from != "container limit" || limit != share(4<<30) {
		t.Fatalf("cgroup of 4 GiB: %d from %q", limit, from)
	}
	if half, err := Parse("50%"); err != nil || half != 2<<30 {
		t.Fatalf("Parse(50%%) in a 4 GiB container = %d, %v", half, err)
	}
}

// share is DefaultShare of n bytes, as Detect works it out.
func share(n int64) int64 { return int64(float64(n) * DefaultShare) }

func TestFormat(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 1536: "1.5 KiB", 3 << 30: "3.0 GiB"} {
		if got := Format(n); got != want {
			t.Errorf("Format(%d) = %q, want %q", n, got, want)
		}
	}
}

// StaysCritical fires only for a stretch of critical memory that lasts
// the grace, and once for it.
func TestStaysCritical(t *testing.T) {
	var mem fake
	g := New(Config{Limit: 1000, Read: mem.read, Log: quiet, KeepRuntimeLimit: true})
	var fired atomic.Int32
	g.StaysCritical(100*time.Millisecond, func() { fired.Add(1) })
	mem.n.Store(950)
	g.Measure()
	time.Sleep(30 * time.Millisecond)
	mem.n.Store(100)
	g.Measure()
	time.Sleep(150 * time.Millisecond)
	if fired.Load() != 0 {
		t.Fatal("fired for a stretch shorter than the grace")
	}
	mem.n.Store(950)
	g.Measure()
	time.Sleep(200 * time.Millisecond)
	g.Measure()
	time.Sleep(150 * time.Millisecond)
	if fired.Load() != 1 {
		t.Fatalf("fired %d times for one long stretch, want 1", fired.Load())
	}
}
