package worker

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// cpuTimes reads the total and idle CPU time from /proc/stat.
func cpuTimes() (total, idle uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, f := range fields[1:] {
		v, _ := strconv.ParseUint(f, 10, 64)
		total += v
		if i == 3 || i == 4 { // idle and iowait
			idle += v
		}
	}
	return total, idle, true
}

// memoryUse reads used and total memory from /proc/meminfo.
func memoryUse() (used, total int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var avail int64
	for line := range strings.SplitSeq(string(b), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
		switch name {
		case "MemTotal":
			total = kb * 1024
		case "MemAvailable":
			avail = kb * 1024
		}
	}
	return total - avail, total
}

// diskUse reads used and total space on the disk holding dir.
func diskUse(dir string) (used, total int64) {
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) != nil {
		return 0, 0
	}
	total = int64(fs.Blocks) * int64(fs.Bsize)
	return total - int64(fs.Bfree)*int64(fs.Bsize), total
}

// loadAverage reads the one-minute load average.
func loadAverage() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	first, _, _ := strings.Cut(string(b), " ")
	v, _ := strconv.ParseFloat(first, 64)
	return v
}
