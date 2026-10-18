package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// The statements below are what storm's typed builder cannot say: a write
// over a set of rows (storm's writes are per row, by key), an upsert that
// adds to the existing value, a time-bucketed average, and a catalog
// function. `storm generate` PREPAREs each one against the model, so a
// renamed column fails the build, and only these exact texts can run.

// Set-based writes.
var (
	// DeleteUserModels clears a user's model grants.
	DeleteUserModels = storm.SQLExec(`DELETE FROM user_models WHERE user_id = $1`)
	// DeleteUserAPIKeys revokes every key a user owns.
	DeleteUserAPIKeys = storm.SQLExec(`DELETE FROM api_keys WHERE user_id = $1`)
	// DeleteUserSessions signs a user out everywhere.
	DeleteUserSessions = storm.SQLExec(`DELETE FROM sessions WHERE user_id = $1`)
	// DeleteOtherSessions signs a user out everywhere but one session.
	DeleteOtherSessions = storm.SQLExec(`DELETE FROM sessions WHERE user_id = $1 AND id <> $2`)
	// DeleteRecoveryCodes drops a user's recovery codes.
	DeleteRecoveryCodes = storm.SQLExec(`DELETE FROM recovery_codes WHERE user_id = $1`)
	// DeleteStaleSessions drops sessions past their end ($1) or idle since
	// before $2.
	DeleteStaleSessions = storm.SQLExec(`DELETE FROM sessions WHERE expire_time <= $1 OR (NOT pending AND last_seen <= $2)`)
	// DeleteExpiredSessions drops sessions past their end.
	DeleteExpiredSessions = storm.SQLExec(`DELETE FROM sessions WHERE expire_time <= $1`)
	// TrimAuditEvents drops audit events older than $1.
	TrimAuditEvents = storm.SQLExec(`DELETE FROM audit_events WHERE "time" < $1`)
	// TrimDeliveries drops finished deliveries made before $1.
	TrimDeliveries = storm.SQLExec(`DELETE FROM deliveries WHERE status <> 1 AND create_time < $1`)
	// TrimWorkerSamples drops worker stats older than $1.
	TrimWorkerSamples = storm.SQLExec(`DELETE FROM worker_samples WHERE "time" < $1`)
	// UnplaceModels sends the models placed on a worker back to automatic.
	UnplaceModels = storm.SQLExec(`UPDATE models SET worker_id = '' WHERE worker_id = $1`)
	// UnplaceRuns sends the runs waiting for a worker back to any worker.
	UnplaceRuns = storm.SQLExec(`UPDATE training_jobs SET run_on = '' WHERE run_on = $1`)
	// RequeueRunning puts jobs a previous process left running ($2) back in
	// the queue ($1).
	RequeueRunning = storm.SQLExec(`UPDATE training_jobs SET status = $1 WHERE status = $2`)
)

// AddUsage adds to a usage counter, creating it at the first prediction.
// The builder's upsert writes the new row's values over the old row's;
// a counter has to add them.
var AddUsage = storm.SQLExec(`INSERT INTO usage (month, user_id, key_id, key_name, predictions)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (month, user_id, key_id)
	DO UPDATE SET predictions = usage.predictions + excluded.predictions, key_name = excluded.key_name`)

// SampleBucket is one averaged slice of a worker's stats.
type SampleBucket struct {
	Time        time.Time
	CpuPercent  float64 // storm matches raw columns to fields by exportName(cpu_percent), not acronym-aware
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
