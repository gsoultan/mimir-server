package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// The statements below are what storm's typed builder cannot say: a
// time-bucketed average and a catalog function. `storm generate` PREPAREs
// each one against the model, so a renamed column fails the build, and
// only these exact texts can run.

// SampleBucket is one averaged slice of a worker's stats.
type SampleBucket struct {
	Time        time.Time
	CPUPercent  float64
	MemoryUsed  int64
	MemoryTotal int64
	DiskUsed    int64
	DiskTotal   int64
	Busy        int64
	Capacity    int64
}

// WorkerSampleBuckets averages a worker's ($1) samples since $2 into
// buckets $3 milliseconds wide, oldest first.
var WorkerSampleBuckets = storm.SQL[SampleBucket](`
	SELECT max("time")                         AS time,
	       avg(cpu_percent)::float8            AS cpu_percent,
	       avg(memory_used)::int8              AS memory_used,
	       max(memory_total)                   AS memory_total,
	       avg(disk_used)::int8                AS disk_used,
	       max(disk_total)                     AS disk_total,
	       round(avg(busy))::int8              AS busy,
	       max(capacity)                       AS capacity
	  FROM worker_samples
	 WHERE worker_id = $1 AND "time" >= $2
	 GROUP BY floor(extract(epoch FROM "time" - $2::timestamptz) * 1000 / $3::int8)
	 ORDER BY 1`)

// DatabaseSize is the size of the server's database on disk.
type DatabaseSize struct {
	Bytes int64
}

// CurrentDatabaseSize reads the size of the database this connection uses.
var CurrentDatabaseSize = storm.SQL[DatabaseSize](`SELECT pg_database_size(current_database()) AS bytes`)
