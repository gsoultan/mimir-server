-- Teams keep groups of people apart on one server: each person belongs to
-- one team and sees only that team's models, examples, training runs,
-- webhooks and audit events. Everything already here joins the first team.
CREATE TABLE teams (
	id          TEXT    PRIMARY KEY,
	name        TEXT    NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 100),
	create_time INTEGER NOT NULL
) STRICT;

INSERT INTO teams (id, name, create_time)
VALUES ('01900000-0000-7000-8000-000000000001', 'My team', CAST(strftime('%s', 'now') AS INTEGER) * 1000);

ALTER TABLE users ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE models ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE datasets ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE training_jobs ADD COLUMN team_id TEXT REFERENCES teams (id);
ALTER TABLE webhooks ADD COLUMN team_id TEXT REFERENCES teams (id) ON DELETE CASCADE;
ALTER TABLE audit_events ADD COLUMN team_id TEXT REFERENCES teams (id) ON DELETE SET NULL;

UPDATE users SET team_id = '01900000-0000-7000-8000-000000000001';
UPDATE models SET team_id = '01900000-0000-7000-8000-000000000001';
UPDATE datasets SET team_id = '01900000-0000-7000-8000-000000000001';
UPDATE training_jobs SET team_id = '01900000-0000-7000-8000-000000000001';
UPDATE webhooks SET team_id = '01900000-0000-7000-8000-000000000001';
UPDATE audit_events SET team_id = '01900000-0000-7000-8000-000000000001';

CREATE INDEX users_team ON users (team_id);
CREATE INDEX models_team ON models (team_id, create_time DESC);
CREATE INDEX datasets_team ON datasets (team_id);
CREATE INDEX training_jobs_team ON training_jobs (team_id);
CREATE INDEX webhooks_team ON webhooks (team_id);
CREATE INDEX audit_events_team ON audit_events (team_id, time DESC, id DESC);
