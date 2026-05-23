-- Stage 3b initial schema. Version 1.
--
-- Two tables:
--   day_counters: monotonic per-day run-id allocator
--   runs:         per-call execution metadata
--
-- The day_counters row for a given day exists from the first run of that
-- day onward; days with no runs have no row. next_seq is "the seq to
-- allocate for the next run today" (so after N runs, next_seq = N+1).
--
-- runs is keyed by (day, seq) for natural per-day grouping. Indexes
-- support the common diagnostic queries: by callable, by status (with a
-- partial index narrowed to 'running' for the common "what's running
-- right now" lookup), and by start time.

CREATE TABLE day_counters (
    day       TEXT PRIMARY KEY,
    next_seq  INTEGER NOT NULL
);

CREATE TABLE runs (
    day            TEXT    NOT NULL,
    seq            INTEGER NOT NULL,
    callable_name  TEXT    NOT NULL,
    callable_kind  TEXT    NOT NULL CHECK (callable_kind IN ('target','function')),
    started_at     INTEGER NOT NULL,                            -- unix ns
    finished_at    INTEGER,                                     -- unix ns; null while running
    duration_ms    INTEGER,                                     -- derived at finish
    status         TEXT    NOT NULL CHECK (status IN ('running','ok','fail','error','killed')),
    exit_code      INTEGER,
    args_json      TEXT,
    prelude_json   TEXT,
    result_json    TEXT,
    source_file    TEXT,
    gmk_pid        INTEGER,
    PRIMARY KEY (day, seq)
);

CREATE INDEX runs_callable_idx ON runs(callable_name);
CREATE INDEX runs_running_idx  ON runs(status) WHERE status = 'running';
CREATE INDEX runs_started_idx  ON runs(started_at);
