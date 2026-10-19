-- The plan `storm diff typed_columns_and_indexes` prints against a database
-- at 0001, with what storm cannot write itself:
--
--   * Lists and documents kept as JSON in text become text[], int8[] and
--     jsonb. storm's ALTER ... TYPE has no USING clause, and PostgreSQL has
--     no cast from a JSON text to an array, so each type change here drops
--     the old text default, converts the value with USING, and sets the new
--     default. "" (how 0001 stored an empty list) becomes an empty one.
--   * storm marks every DROP INDEX, the dropped CHECK and the text -> jsonb /
--     int8[] changes as destructive and will not write the file without
--     -allow-destructive. Each was read: the indexes are replaced by the
--     ones created beside them, the CHECK by an equivalent one, and the
--     conversions keep every value.
--
-- The server runs this file itself on a database at 0001 (store.upgrade),
-- before migrate.Auto, which then has nothing left to do.

-- PostgreSQL refuses a subquery in USING, so the JSON-to-array conversions
-- are functions; pg_temp keeps them out of the schema and they go at the end.
CREATE FUNCTION pg_temp.json_texts(s text) RETURNS text[] LANGUAGE sql IMMUTABLE AS $$
    SELECT coalesce(array_agg(e ORDER BY n), '{}')
      FROM jsonb_array_elements_text(
               CASE WHEN jsonb_typeof(coalesce(nullif(s, ''), '[]')::jsonb) = 'array'
                    THEN coalesce(nullif(s, ''), '[]')::jsonb ELSE '[]' END
           ) WITH ORDINALITY AS x(e, n)
$$;
CREATE FUNCTION pg_temp.json_int8s(s text) RETURNS int8[] LANGUAGE sql IMMUTABLE AS $$
    SELECT coalesce(array_agg(e::int8 ORDER BY n), '{}')
      FROM unnest(pg_temp.json_texts(s)) WITH ORDINALITY AS x(e, n)
$$;
CREATE FUNCTION pg_temp.json_doc(s text) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT coalesce(nullif(nullif(s, ''), 'null'), '[]')::jsonb
$$;

-- Typed columns.
ALTER TABLE "models"
    ALTER COLUMN "input_shape" DROP DEFAULT,
    ALTER COLUMN "input_shape" TYPE int8[] USING pg_temp.json_int8s("input_shape"),
    ALTER COLUMN "input_shape" SET DEFAULT '{}'::bigint[],
    ALTER COLUMN "labels" DROP DEFAULT,
    ALTER COLUMN "labels" TYPE text[] USING pg_temp.json_texts("labels"),
    ALTER COLUMN "labels" SET DEFAULT '{}'::text[],
    ALTER COLUMN "features" DROP DEFAULT,
    ALTER COLUMN "features" TYPE text[] USING pg_temp.json_texts("features"),
    ALTER COLUMN "features" SET DEFAULT '{}'::text[],
    ALTER COLUMN "columns" DROP DEFAULT,
    ALTER COLUMN "columns" TYPE jsonb USING pg_temp.json_doc("columns"),
    ALTER COLUMN "columns" SET DEFAULT '[]'::jsonb;
ALTER TABLE "datasets"
    ALTER COLUMN "features" DROP DEFAULT,
    ALTER COLUMN "features" TYPE text[] USING pg_temp.json_texts("features"),
    ALTER COLUMN "features" SET DEFAULT '{}'::text[],
    ALTER COLUMN "labels" DROP DEFAULT,
    ALTER COLUMN "labels" TYPE text[] USING pg_temp.json_texts("labels"),
    ALTER COLUMN "labels" SET DEFAULT '{}'::text[],
    ALTER COLUMN "input_shape" DROP DEFAULT,
    ALTER COLUMN "input_shape" TYPE int8[] USING pg_temp.json_int8s("input_shape"),
    ALTER COLUMN "input_shape" SET DEFAULT '{}'::bigint[],
    ALTER COLUMN "columns" DROP DEFAULT,
    ALTER COLUMN "columns" TYPE jsonb USING pg_temp.json_doc("columns"),
    ALTER COLUMN "columns" SET DEFAULT '[]'::jsonb;
ALTER TABLE "training_jobs"
    ALTER COLUMN "history" DROP DEFAULT,
    ALTER COLUMN "history" TYPE jsonb USING pg_temp.json_doc("history"),
    ALTER COLUMN "history" SET DEFAULT '[]'::jsonb;
ALTER TABLE "webhooks"
    ALTER COLUMN "events" DROP DEFAULT,
    ALTER COLUMN "events" TYPE text[] USING pg_temp.json_texts("events"),
    ALTER COLUMN "events" SET DEFAULT '{}'::text[];

DROP FUNCTION pg_temp.json_doc(text);
DROP FUNCTION pg_temp.json_int8s(text);
DROP FUNCTION pg_temp.json_texts(text);

-- The dataset kind CHECK, now built from the DatasetKind constants.
ALTER TABLE "datasets" ADD CONSTRAINT "ck_datasets_kind_between_1_and_4" CHECK ((kind >= 1) AND (kind <= 4));
ALTER TABLE "datasets" DROP CONSTRAINT "ck_datasets_kind_in_1_2_3_4";

-- Indexes, each matched to the queries in package store.
CREATE INDEX "ix_api_keys_user_id_create_time_id" ON "api_keys" ("user_id", "create_time" DESC, "id" DESC);
DROP INDEX "ix_api_keys_user_id";

CREATE INDEX "ix_audit_events_target_time_id" ON "audit_events" ("target", "time" DESC, "id" DESC);
CREATE INDEX "ix_audit_events_team_id_id" ON "audit_events" ("team_id", "id" DESC);
DROP INDEX "ix_audit_events_target";
DROP INDEX "ix_audit_events_team_id_time_id";
DROP INDEX "ix_audit_events_time_id";
CREATE INDEX "ix_audit_events_time" ON "audit_events" USING brin ("time");

CREATE INDEX "ix_datasets_team_id_create_time_id" ON "datasets" ("team_id", "create_time" DESC, "id" DESC);
DROP INDEX "ix_datasets_team_id_create_time";

DROP INDEX "ix_deliveries_create_time";
CREATE INDEX "ix_deliveries_create_time" ON "deliveries" USING brin ("create_time");
CREATE INDEX "ix_deliveries_next_attempt_id" ON "deliveries" ("next_attempt", "id") WHERE status = 1;
DROP INDEX "ix_deliveries_next_attempt";

CREATE INDEX "ix_models_team_id_create_time_id" ON "models" ("team_id", "create_time" DESC, "id" DESC);
DROP INDEX "ix_models_create_time";
DROP INDEX "ix_models_team_id_create_time";

CREATE INDEX "ix_training_jobs_next_run_id" ON "training_jobs" ("next_run", "id") WHERE repeat_seconds > 0;
CREATE INDEX "ix_training_jobs_run_on" ON "training_jobs" ("run_on");
CREATE INDEX "ix_training_jobs_status_create_time_id" ON "training_jobs" ("status", "create_time", "id");
CREATE INDEX "ix_training_jobs_team_id_create_time_id" ON "training_jobs" ("team_id", "create_time" DESC, "id" DESC);
CREATE INDEX "ix_training_jobs_worker" ON "training_jobs" ("worker");
DROP INDEX "ix_training_jobs_create_time";
DROP INDEX "ix_training_jobs_next_run";
DROP INDEX "ix_training_jobs_status";
DROP INDEX "ix_training_jobs_team_id_create_time";

CREATE INDEX "ix_users_team_id_create_time_id" ON "users" ("team_id", "create_time", "id");
DROP INDEX "ix_users_team_id";

CREATE INDEX "ix_webhooks_team_id_create_time_id" ON "webhooks" ("team_id", "create_time", "id");
DROP INDEX "ix_webhooks_team_id";

DROP INDEX "ix_worker_samples_time";
CREATE INDEX "ix_worker_samples_time" ON "worker_samples" USING brin ("time");
