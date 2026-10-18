-- Predictions counted per month, person and key ('' for the web app), for
-- monthly limits and the usage page.
CREATE TABLE usage (
	month       TEXT    NOT NULL CHECK (length(month) = 7),
	user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	key_id      TEXT    NOT NULL DEFAULT '',
	key_name    TEXT    NOT NULL DEFAULT '',
	predictions INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (month, user_id, key_id)
) STRICT;
