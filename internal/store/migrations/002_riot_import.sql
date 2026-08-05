CREATE TABLE sync_runs (
    id                  INTEGER PRIMARY KEY,
    player_id           INTEGER REFERENCES player_profiles(id) ON DELETE CASCADE,
    trigger_source      TEXT NOT NULL
                            CHECK (trigger_source IN ('startup', 'manual', 'poll')),
    state               TEXT NOT NULL DEFAULT 'running'
                            CHECK (state IN ('running', 'succeeded', 'partial', 'failed')),
    discovered_count    INTEGER NOT NULL DEFAULT 0 CHECK (discovered_count >= 0),
    imported_count      INTEGER NOT NULL DEFAULT 0 CHECK (imported_count >= 0),
    skipped_count       INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    failed_count        INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    error_code          TEXT NOT NULL DEFAULT '' CHECK (length(error_code) <= 64),
    error_message       TEXT NOT NULL DEFAULT '' CHECK (length(error_message) <= 1000),
    started_at          INTEGER NOT NULL,
    completed_at        INTEGER
);

CREATE INDEX sync_runs_player_started
    ON sync_runs(player_id, started_at DESC, id DESC);

CREATE TABLE import_jobs (
    id                  INTEGER PRIMARY KEY,
    player_id           INTEGER NOT NULL REFERENCES player_profiles(id) ON DELETE CASCADE,
    riot_match_id       TEXT NOT NULL CHECK (length(trim(riot_match_id)) > 0),
    last_sync_run_id    INTEGER REFERENCES sync_runs(id) ON DELETE SET NULL,
    state               TEXT NOT NULL DEFAULT 'pending'
                            CHECK (state IN (
                                'pending',
                                'fetching_detail',
                                'detail_stored',
                                'detail_only',
                                'fetching_timeline',
                                'complete',
                                'partial_timeline',
                                'retry_wait',
                                'failed'
                            )),
    resume_step         TEXT NOT NULL DEFAULT 'detail'
                            CHECK (resume_step IN (
                                'detail',
                                'normalize_detail',
                                'timeline',
                                'normalize_timeline',
                                'done'
                            )),
    attempt_count       INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at     INTEGER,
    error_code          TEXT NOT NULL DEFAULT '' CHECK (length(error_code) <= 64),
    error_message       TEXT NOT NULL DEFAULT '' CHECK (length(error_message) <= 1000),
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    UNIQUE (player_id, riot_match_id)
);

CREATE INDEX import_jobs_ready
    ON import_jobs(player_id, state, next_attempt_at, updated_at);

CREATE TABLE api_payloads (
    id                  INTEGER PRIMARY KEY,
    import_job_id       INTEGER NOT NULL REFERENCES import_jobs(id) ON DELETE CASCADE,
    payload_kind        TEXT NOT NULL CHECK (payload_kind IN ('match', 'timeline')),
    revision            INTEGER NOT NULL CHECK (revision >= 1),
    sha256              TEXT NOT NULL CHECK (
                            length(sha256) = 64
                            AND sha256 NOT GLOB '*[^0-9a-f]*'
                        ),
    content_encoding    TEXT NOT NULL DEFAULT 'gzip'
                            CHECK (content_encoding = 'gzip'),
    payload             BLOB NOT NULL,
    http_status         INTEGER NOT NULL CHECK (http_status BETWEEN 100 AND 599),
    fetched_at          INTEGER NOT NULL,
    is_current          INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
    UNIQUE (import_job_id, payload_kind, revision),
    UNIQUE (import_job_id, payload_kind, sha256)
);

CREATE UNIQUE INDEX one_current_api_payload
    ON api_payloads(import_job_id, payload_kind)
    WHERE is_current = 1;

CREATE INDEX api_payloads_job_kind_revision
    ON api_payloads(import_job_id, payload_kind, revision DESC);
