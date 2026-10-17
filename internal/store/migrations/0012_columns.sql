-- rebuild: foreign keys off
-- Tables can hold categories, yes/no, free text and photo names, not only
-- numbers, and datasets can be photos: kind 3 is a ZIP of photo folders,
-- kind 4 a ZIP of a data.csv and its photos. The kind check is widened by
-- rebuilding the table, as SQLite's guide to table changes describes.
CREATE TABLE datasets_new (
	id            TEXT    PRIMARY KEY,
	name          TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	-- kind: 1 table (CSV), 2 mnist, 3 photo folders (ZIP), 4 table and photos (ZIP)
	kind          INTEGER NOT NULL CHECK (kind IN (1, 2, 3, 4)),
	rows          INTEGER NOT NULL,
	-- features, labels and input_shape are JSON arrays.
	features      TEXT    NOT NULL DEFAULT '[]',
	labels        TEXT    NOT NULL DEFAULT '[]',
	input_shape   TEXT    NOT NULL DEFAULT '[]',
	target        TEXT    NOT NULL DEFAULT '',
	size_bytes    INTEGER NOT NULL DEFAULT 0,
	create_time   INTEGER NOT NULL,
	driver        TEXT    NOT NULL DEFAULT '',
	dsn           TEXT    NOT NULL DEFAULT '',
	query         TEXT    NOT NULL DEFAULT '',
	refresh_time  INTEGER,
	team_id       TEXT    REFERENCES teams (id),
	-- columns is the input columns and how each becomes numbers, as JSON;
	-- '[]' means every column is a number.
	columns       TEXT    NOT NULL DEFAULT '[]',
	-- number_target says every answer is a number, so a number model fits.
	number_target INTEGER NOT NULL DEFAULT 0,
	-- skipped counts rows left out: no answer, or a missing photo.
	skipped       INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO datasets_new (id, name, kind, rows, features, labels, input_shape, target, size_bytes, create_time, driver, dsn, query, refresh_time, team_id)
	SELECT id, name, kind, rows, features, labels, input_shape, target, size_bytes, create_time, driver, dsn, query, refresh_time, team_id FROM datasets;
DROP TABLE datasets;
ALTER TABLE datasets_new RENAME TO datasets;
CREATE INDEX datasets_team ON datasets (team_id);

-- A model taught from a table keeps its columns the same way.
ALTER TABLE models ADD COLUMN columns TEXT NOT NULL DEFAULT '[]';
