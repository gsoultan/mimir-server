package worker

import (
	"sync"
	"time"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// sampler measures the machine for the console's dashboard. CPU use is
// the share of time busy since the last reading.
type sampler struct {
	dir     string
	started time.Time

	mu          sync.Mutex
	total, idle uint64
	at          time.Time // when total and idle were read
	last        float64
}

func newSampler(dir string) *sampler {
	s := &sampler{dir: dir, started: time.Now()}
	s.total, s.idle, _ = cpuTimes()
	s.at = s.started
	return s
}

func (s *sampler) read() *mimirv1.Stats {
	st := &mimirv1.Stats{UptimeSeconds: int64(time.Since(s.started).Seconds()), LoadAverage: loadAverage()}
	if total, idle, ok := cpuTimes(); ok {
		s.mu.Lock()
		// A window under a couple of seconds is mostly noise; report the
		// last reading until there's a longer one.
		if time.Since(s.at) >= 2*time.Second {
			if dt := total - s.total; dt > 0 && total >= s.total && idle >= s.idle {
				s.last = 100 * float64(dt-min(idle-s.idle, dt)) / float64(dt)
			}
			s.total, s.idle, s.at = total, idle, time.Now()
		}
		st.CpuPercent = s.last
		s.mu.Unlock()
	}
	st.MemoryUsedBytes, st.MemoryTotalBytes = memoryUse()
	if st.MemoryTotalBytes == 0 {
		st.MemoryTotalBytes = memory()
	}
	st.DiskUsedBytes, st.DiskTotalBytes = diskUse(s.dir)
	return st
}
