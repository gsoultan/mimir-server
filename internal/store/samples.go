package store

import (
	"math"
	"time"
)

// SampleSeries is a worker's samples, oldest first.
type SampleSeries []WorkerSample

// Buckets averages the series into buckets width wide, counted from since,
// oldest first, leaving out the empty ones. Each bucket takes the time of
// its last sample, the mean CPU, memory and disk use and busy slots, and
// the largest totals and capacity, as the dashboard charts use against
// those totals. It reads the series in order and does not change it.
func (ss SampleSeries) Buckets(since time.Time, width time.Duration) SampleSeries {
	width = max(width, time.Millisecond)
	var out SampleSeries
	var acc bucket
	for _, x := range ss {
		i := int64(x.Time.Sub(since) / width)
		if acc.n > 0 && i != acc.index {
			out = append(out, acc.mean())
			acc = bucket{}
		}
		acc.add(i, x)
	}
	if acc.n > 0 {
		out = append(out, acc.mean())
	}
	return out
}

// bucket sums the samples of one bucket.
type bucket struct {
	index                       int64
	n                           int
	last                        time.Time
	cpu, memory, disk, busy     float64
	memoryTotal, diskTotal, cap int64
}

func (b *bucket) add(index int64, x WorkerSample) {
	b.index = index
	b.n++
	if x.Time.After(b.last) {
		b.last = x.Time
	}
	b.cpu += x.CPUPercent
	b.memory += float64(x.MemoryUsed)
	b.disk += float64(x.DiskUsed)
	b.busy += float64(x.Busy)
	b.memoryTotal = max(b.memoryTotal, x.MemoryTotal)
	b.diskTotal = max(b.diskTotal, x.DiskTotal)
	b.cap = max(b.cap, int64(x.Capacity))
}

func (b *bucket) mean() WorkerSample {
	n := float64(b.n)
	return WorkerSample{
		Time:        b.last,
		CPUPercent:  b.cpu / n,
		MemoryUsed:  int64(math.Round(b.memory / n)),
		MemoryTotal: b.memoryTotal,
		DiskUsed:    int64(math.Round(b.disk / n)),
		DiskTotal:   b.diskTotal,
		Busy:        int(math.Round(b.busy / n)),
		Capacity:    int(b.cap),
	}
}
