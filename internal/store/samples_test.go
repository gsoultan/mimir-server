package store

import (
	"testing"
	"time"
)

func TestSampleSeriesBuckets(t *testing.T) {
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return since.Add(time.Duration(min) * time.Minute) }
	ss := SampleSeries{
		{Time: at(0), CPUPercent: 10, MemoryUsed: 100, MemoryTotal: 1000, DiskUsed: 5, DiskTotal: 50, Busy: 1, Capacity: 2},
		{Time: at(1), CPUPercent: 30, MemoryUsed: 201, MemoryTotal: 1000, DiskUsed: 6, DiskTotal: 60, Busy: 2, Capacity: 4},
		{Time: at(5), CPUPercent: 50, MemoryUsed: 300, MemoryTotal: 900, DiskUsed: 7, DiskTotal: 50, Busy: 0, Capacity: 2},
	}
	got := ss.Buckets(since, 2*time.Minute)
	want := SampleSeries{
		{Time: at(1), CPUPercent: 20, MemoryUsed: 151, MemoryTotal: 1000, DiskUsed: 6, DiskTotal: 60, Busy: 2, Capacity: 4},
		{Time: at(5), CPUPercent: 50, MemoryUsed: 300, MemoryTotal: 900, DiskUsed: 7, DiskTotal: 50, Busy: 0, Capacity: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d buckets, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(SampleSeries(nil).Buckets(since, time.Minute)) != 0 {
		t.Error("an empty series has buckets")
	}
}
