CREATE TABLE runs (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  suite       TEXT NOT NULL,
  branch      TEXT NOT NULL,
  commit_sha  TEXT NOT NULL,
  started_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX runs_suite_started ON runs (suite, started_at DESC);
CREATE INDEX runs_started ON runs (started_at DESC, id DESC);

CREATE TABLE results (
  id            BIGSERIAL PRIMARY KEY,
  run_id        UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('PASSED', 'FAILED', 'SKIPPED')),
  duration_ms   BIGINT NOT NULL CHECK (duration_ms >= 0),
  error_message TEXT,
  recorded_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX results_run ON results (run_id);
