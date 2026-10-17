-- Models and training runs can be placed on one worker. Empty means any.
ALTER TABLE models ADD COLUMN worker_id TEXT NOT NULL DEFAULT '';
ALTER TABLE training_jobs ADD COLUMN run_on TEXT NOT NULL DEFAULT '';

-- A worker proves itself with the key it first joined with, and the
-- console remembers where it last connected from.
ALTER TABLE workers ADD COLUMN public_key BLOB;
ALTER TABLE workers ADD COLUMN last_address TEXT NOT NULL DEFAULT '';

-- One sample a minute of each worker's stats, for its dashboard charts.
CREATE TABLE worker_samples (
	worker_id    TEXT    NOT NULL REFERENCES workers (id) ON DELETE CASCADE,
	time         INTEGER NOT NULL,
	cpu_percent  REAL    NOT NULL,
	memory_used  INTEGER NOT NULL,
	memory_total INTEGER NOT NULL,
	disk_used    INTEGER NOT NULL,
	disk_total   INTEGER NOT NULL,
	busy         INTEGER NOT NULL,
	capacity     INTEGER NOT NULL,
	PRIMARY KEY (worker_id, time)
) STRICT, WITHOUT ROWID;
