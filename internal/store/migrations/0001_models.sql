CREATE TABLE models (
	id           TEXT    PRIMARY KEY,
	name         TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	description  TEXT    NOT NULL DEFAULT '' CHECK (length(description) <= 500),
	architecture TEXT    NOT NULL CHECK (architecture <> ''),
	size_bytes   INTEGER NOT NULL CHECK (size_bytes > 0),
	sha256       TEXT    NOT NULL CHECK (length(sha256) = 64),
	create_time  INTEGER NOT NULL,
	update_time  INTEGER NOT NULL
) STRICT;

CREATE INDEX models_create_time ON models (create_time DESC);
