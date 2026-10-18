-- A model taught here takes its shape and labels from its dataset, so the
-- models table records them. Uploaded models of a fixed architecture leave
-- them empty.
ALTER TABLE models ADD COLUMN input_shape TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN labels TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN features TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN job_id TEXT NOT NULL DEFAULT '';

-- datasets: the examples. A table's CSV lives at <data>/datasets/<id>.csv;
-- the built-in sets are downloaded under <data>/datasets/<kind>/.
CREATE TABLE datasets (
	id          TEXT    PRIMARY KEY,
	name        TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	-- kind: 1 table (CSV), 2 mnist
	kind        INTEGER NOT NULL CHECK (kind IN (1, 2)),
	rows        INTEGER NOT NULL,
	-- features, labels and input_shape are JSON arrays.
	features    TEXT    NOT NULL DEFAULT '[]',
	labels      TEXT    NOT NULL DEFAULT '[]',
	input_shape TEXT    NOT NULL DEFAULT '[]',
	target      TEXT    NOT NULL DEFAULT '',
	size_bytes  INTEGER NOT NULL DEFAULT 0,
	create_time INTEGER NOT NULL
) STRICT;

-- training_jobs: one row per run. history is a JSON array of epochs.
CREATE TABLE training_jobs (
	id              TEXT    PRIMARY KEY,
	name            TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	description     TEXT    NOT NULL DEFAULT '',
	architecture    TEXT    NOT NULL,
	dataset_id      TEXT    NOT NULL REFERENCES datasets (id) ON DELETE RESTRICT,
	epochs          INTEGER NOT NULL,
	batch_size      INTEGER NOT NULL,
	learning_rate   REAL    NOT NULL,
	hold_out        REAL    NOT NULL,
	-- status: 1 queued, 2 running, 3 succeeded, 4 failed, 5 cancelled
	status          INTEGER NOT NULL CHECK (status BETWEEN 1 AND 5),
	error           TEXT    NOT NULL DEFAULT '',
	epoch           INTEGER NOT NULL DEFAULT 0,
	step            INTEGER NOT NULL DEFAULT 0,
	steps_per_epoch INTEGER NOT NULL DEFAULT 0,
	history         TEXT    NOT NULL DEFAULT '[]',
	model_id        TEXT    NOT NULL DEFAULT '',
	created_by      TEXT    NOT NULL DEFAULT '',
	create_time     INTEGER NOT NULL,
	start_time      INTEGER,
	end_time        INTEGER
) STRICT;

CREATE INDEX training_jobs_create_time ON training_jobs (create_time DESC);
CREATE INDEX training_jobs_status ON training_jobs (status);
