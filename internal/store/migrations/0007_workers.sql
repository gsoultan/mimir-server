-- Workers are processes that run models for this server. Only the hash of
-- a worker's token is kept; what it can do is learned when it connects.
CREATE TABLE workers (
	id          TEXT    PRIMARY KEY,
	name        TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	token_hash  TEXT    NOT NULL UNIQUE,
	created_by  TEXT    NOT NULL,
	create_time INTEGER NOT NULL,
	last_seen   INTEGER
) STRICT;

ALTER TABLE training_jobs ADD COLUMN worker TEXT NOT NULL DEFAULT '';
