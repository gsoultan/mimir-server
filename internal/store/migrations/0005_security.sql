-- Sessions get an id, the browser they belong to and an idle clock, and a
-- pending state for the gap between password and second factor. Rebuilding
-- the table signs everyone out once, which is fine for this update.
DROP TABLE sessions;

CREATE TABLE sessions (
	id          TEXT    PRIMARY KEY,
	token_hash  TEXT    NOT NULL UNIQUE,
	user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	-- agent_hash binds the session to the browser that started it.
	agent_hash  TEXT    NOT NULL,
	device      TEXT    NOT NULL DEFAULT '',
	ip          TEXT    NOT NULL DEFAULT '',
	-- pending is 1 while a sign-in still waits for its second factor.
	pending     INTEGER NOT NULL DEFAULT 0 CHECK (pending IN (0, 1)),
	create_time INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	expire_time INTEGER NOT NULL
) STRICT;

CREATE INDEX sessions_user ON sessions (user_id);

-- totp_secret is the confirmed authenticator key; totp_pending is one that
-- was shown but not yet confirmed with a code. totp_last_step refuses a
-- code that was already used.
ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_pending TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;

-- One-time codes for when the phone is gone. Stored hashed.
CREATE TABLE recovery_codes (
	user_id   TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	code_hash TEXT NOT NULL,
	PRIMARY KEY (user_id, code_hash)
) STRICT;
