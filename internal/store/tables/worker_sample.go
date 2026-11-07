// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// WorkerSample is one minute of a worker's stats, for its charts.
type WorkerSample struct {
	Worker      Worker
	Time        time.Time
	CPUPercent  float64
	MemoryUsed  int64
	MemoryTotal int64
	DiskUsed    int64
	DiskTotal   int64
	Busy        int64
	Capacity    int64
}

func (s *WorkerSample) Schema(t *storm.Table) {
	// The key serves a worker's chart (worker, time range) and the
	// worker's cascade.
	t.PrimaryKey(&s.Worker, &s.Time)
	t.Col(&s.Worker).OnDelete(storm.Cascade).NoIndex()
	// The retention trim across every worker; rows arrive in time order.
	t.Index(&s.Time).Using(storm.BRIN)
}
