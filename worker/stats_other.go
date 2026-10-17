//go:build !linux

package worker

// Elsewhere the worker reports what Go knows without asking the OS.

func cpuTimes() (total, idle uint64, ok bool) { return 0, 0, false }

func memoryUse() (used, total int64) { return 0, 0 }

func diskUse(dir string) (used, total int64) { return 0, 0 }

func loadAverage() float64 { return 0 }
