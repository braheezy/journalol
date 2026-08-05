CREATE TABLE death_clips (
    id                  INTEGER PRIMARY KEY,
    match_id            INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    timeline_sequence   INTEGER NOT NULL CHECK (timeline_sequence >= 0),
    death_index         INTEGER NOT NULL CHECK (death_index >= 1),
    death_timestamp_ms  INTEGER NOT NULL CHECK (death_timestamp_ms >= 0),
    start_timestamp_ms  INTEGER NOT NULL CHECK (start_timestamp_ms >= 0),
    end_timestamp_ms    INTEGER NOT NULL CHECK (end_timestamp_ms > start_timestamp_ms),
    replay_path         TEXT NOT NULL,
    output_path         TEXT NOT NULL,
    codec               TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (status IN ('recording', 'ready', 'failed')),
    error_message       TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    UNIQUE (match_id, timeline_sequence)
);

CREATE INDEX death_clips_match_status
    ON death_clips(match_id, status, death_index);
