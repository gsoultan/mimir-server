CREATE TABLE webhooks (
	id          TEXT    PRIMARY KEY,
	name        TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	url         TEXT    NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
	-- secret signs deliveries. It is kept as is, because signing needs it.
	secret      TEXT    NOT NULL CHECK (secret <> ''),
	-- events is a JSON array of event types; [] means every event.
	events      TEXT    NOT NULL DEFAULT '[]',
	enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
	create_time INTEGER NOT NULL
) STRICT;

-- One row per delivery: an outbox while pending, history afterwards.
-- status: 1 pending, 2 delivered, 3 failed
CREATE TABLE deliveries (
	id            TEXT    PRIMARY KEY,
	webhook_id    TEXT    NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
	event_id      TEXT    NOT NULL,
	event_type    TEXT    NOT NULL,
	payload       TEXT    NOT NULL,
	status        INTEGER NOT NULL CHECK (status BETWEEN 1 AND 3),
	attempts      INTEGER NOT NULL DEFAULT 0,
	response_code INTEGER NOT NULL DEFAULT 0,
	error         TEXT    NOT NULL DEFAULT '',
	create_time   INTEGER NOT NULL,
	last_attempt  INTEGER,
	next_attempt  INTEGER
) STRICT;

CREATE INDEX deliveries_due ON deliveries (next_attempt) WHERE status = 1;
CREATE INDEX deliveries_webhook ON deliveries (webhook_id, id DESC);
