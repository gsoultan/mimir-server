-- A dataset can come from a database query, kept so it can be fetched
-- again. The connection string is a secret: never sent to clients.
ALTER TABLE datasets ADD COLUMN driver TEXT NOT NULL DEFAULT '';
ALTER TABLE datasets ADD COLUMN dsn TEXT NOT NULL DEFAULT '';
ALTER TABLE datasets ADD COLUMN query TEXT NOT NULL DEFAULT '';
ALTER TABLE datasets ADD COLUMN refresh_time INTEGER;

-- A job can keep learning: on a schedule, a new pass fetches the examples
-- again and continues from the model. Later passes point at the first.
ALTER TABLE training_jobs ADD COLUMN repeat_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE training_jobs ADD COLUMN next_run INTEGER;
ALTER TABLE training_jobs ADD COLUMN parent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE training_jobs ADD COLUMN base_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE training_jobs ADD COLUMN note TEXT NOT NULL DEFAULT '';

-- What a taught model scored, so the list can say it and a later pass
-- can tell whether it did better.
ALTER TABLE models ADD COLUMN accuracy REAL NOT NULL DEFAULT 0;
