CREATE TABLE users (
	id            TEXT    PRIMARY KEY,
	email         TEXT    NOT NULL UNIQUE CHECK (length(email) BETWEEN 3 AND 254),
	name          TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	password_hash TEXT    NOT NULL CHECK (password_hash <> ''),
	-- role: 1 viewer, 2 editor, 3 admin, 4 owner
	role          INTEGER NOT NULL CHECK (role BETWEEN 1 AND 4),
	all_models    INTEGER NOT NULL CHECK (all_models IN (0, 1)),
	disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
	create_time   INTEGER NOT NULL,
	last_sign_in  INTEGER
) STRICT;

-- The models a user may reach when all_models is 0. A deleted model drops
-- out of every grant.
CREATE TABLE user_models (
	user_id  TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	model_id TEXT NOT NULL REFERENCES models (id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, model_id)
) STRICT;

CREATE INDEX user_models_model ON user_models (model_id);

CREATE TABLE sessions (
	token_hash  TEXT    PRIMARY KEY,
	user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	create_time INTEGER NOT NULL,
	expire_time INTEGER NOT NULL
) STRICT;

CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE api_keys (
	id          TEXT    PRIMARY KEY,
	user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	name        TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	prefix      TEXT    NOT NULL,
	secret_hash TEXT    NOT NULL UNIQUE,
	role        INTEGER NOT NULL CHECK (role BETWEEN 1 AND 4),
	all_models  INTEGER NOT NULL CHECK (all_models IN (0, 1)),
	create_time INTEGER NOT NULL,
	last_used   INTEGER
) STRICT;

CREATE INDEX api_keys_user ON api_keys (user_id);

CREATE TABLE api_key_models (
	key_id   TEXT NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
	model_id TEXT NOT NULL REFERENCES models (id) ON DELETE CASCADE,
	PRIMARY KEY (key_id, model_id)
) STRICT;

CREATE INDEX api_key_models_model ON api_key_models (model_id);

-- Who did what. Rows are never updated; retention trims old ones.
CREATE TABLE audit_events (
	id     TEXT    PRIMARY KEY,
	time   INTEGER NOT NULL,
	actor  TEXT    NOT NULL,
	action TEXT    NOT NULL,
	target TEXT    NOT NULL DEFAULT '',
	detail TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX audit_events_time ON audit_events (time DESC, id DESC);
