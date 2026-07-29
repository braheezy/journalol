CREATE TABLE player_profiles (
    id                  INTEGER PRIMARY KEY,
    game_name           TEXT NOT NULL,
    tag_line            TEXT NOT NULL,
    platform_route      TEXT NOT NULL,
    regional_route      TEXT NOT NULL,
    puuid                TEXT NOT NULL UNIQUE,
    profile_icon_id     INTEGER,
    summoner_level      INTEGER,
    is_primary          INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
    is_demo             INTEGER NOT NULL DEFAULT 0 CHECK (is_demo IN (0, 1)),
    poll_interval_mins  INTEGER NOT NULL DEFAULT 5 CHECK (poll_interval_mins >= 1),
    history_limit       INTEGER NOT NULL DEFAULT 20 CHECK (history_limit BETWEEN 1 AND 100),
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE UNIQUE INDEX one_primary_player
    ON player_profiles(is_primary)
    WHERE is_primary = 1;

CREATE TABLE matches (
    id                  INTEGER PRIMARY KEY,
    riot_match_id       TEXT NOT NULL UNIQUE,
    queue_id            INTEGER NOT NULL DEFAULT 0,
    queue_type          TEXT NOT NULL DEFAULT 'Unknown',
    map_id              INTEGER NOT NULL DEFAULT 0,
    game_mode           TEXT NOT NULL DEFAULT '',
    game_type           TEXT NOT NULL DEFAULT '',
    patch               TEXT NOT NULL DEFAULT '',
    game_start_at       INTEGER NOT NULL,
    game_end_at         INTEGER NOT NULL,
    duration_seconds    INTEGER NOT NULL CHECK (duration_seconds >= 0),
    is_remake           INTEGER NOT NULL DEFAULT 0 CHECK (is_remake IN (0, 1)),
    surrendered         INTEGER NOT NULL DEFAULT 0 CHECK (surrendered IN (0, 1)),
    completeness        TEXT NOT NULL DEFAULT 'complete'
                            CHECK (completeness IN ('detail_only', 'complete', 'partial_timeline')),
    normalizer_version  INTEGER NOT NULL DEFAULT 1 CHECK (normalizer_version >= 1),
    imported_at         INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE INDEX matches_start_time ON matches(game_start_at DESC);

CREATE TABLE player_match_stats (
    match_id                    INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    player_id                   INTEGER NOT NULL REFERENCES player_profiles(id) ON DELETE CASCADE,
    participant_id              INTEGER NOT NULL DEFAULT 0,
    team_id                     INTEGER NOT NULL DEFAULT 0,
    champion_id                 INTEGER NOT NULL DEFAULT 0,
    champion_name               TEXT NOT NULL,
    role                        TEXT NOT NULL DEFAULT 'UNKNOWN',
    win                         INTEGER NOT NULL CHECK (win IN (0, 1)),
    kills                       INTEGER NOT NULL DEFAULT 0 CHECK (kills >= 0),
    deaths                      INTEGER NOT NULL DEFAULT 0 CHECK (deaths >= 0),
    assists                     INTEGER NOT NULL DEFAULT 0 CHECK (assists >= 0),
    lane_minions                INTEGER NOT NULL DEFAULT 0 CHECK (lane_minions >= 0),
    neutral_minions             INTEGER NOT NULL DEFAULT 0 CHECK (neutral_minions >= 0),
    gold                        INTEGER NOT NULL DEFAULT 0 CHECK (gold >= 0),
    champion_damage             INTEGER NOT NULL DEFAULT 0 CHECK (champion_damage >= 0),
    vision_score                INTEGER NOT NULL DEFAULT 0 CHECK (vision_score >= 0),
    wards_placed                INTEGER NOT NULL DEFAULT 0 CHECK (wards_placed >= 0),
    wards_killed                INTEGER NOT NULL DEFAULT 0 CHECK (wards_killed >= 0),
    vision_wards_bought         INTEGER NOT NULL DEFAULT 0 CHECK (vision_wards_bought >= 0),
    opponent_champion           TEXT NOT NULL DEFAULT '',
    final_items_json            TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(final_items_json)),
    runes_json                  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(runes_json)),
    summoner_spells_json        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(summoner_spells_json)),
    skill_order_json            TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(skill_order_json)),
    PRIMARY KEY (match_id, player_id)
);

CREATE INDEX player_match_stats_player
    ON player_match_stats(player_id, match_id);

CREATE TABLE training_blocks (
    id                  INTEGER PRIMARY KEY,
    player_id           INTEGER NOT NULL REFERENCES player_profiles(id) ON DELETE CASCADE,
    name                TEXT NOT NULL CHECK (length(trim(name)) > 0),
    description         TEXT NOT NULL DEFAULT '',
    start_date          TEXT NOT NULL CHECK (start_date GLOB '????-??-??'),
    end_date            TEXT CHECK (end_date IS NULL OR end_date GLOB '????-??-??'),
    status              TEXT NOT NULL DEFAULT 'planned'
                            CHECK (status IN ('planned', 'active', 'completed', 'abandoned')),
    reminder            TEXT NOT NULL DEFAULT '',
    notes               TEXT NOT NULL DEFAULT '',
    retrospective       TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    CHECK (end_date IS NULL OR end_date >= start_date)
);

CREATE UNIQUE INDEX one_active_training_block_per_player
    ON training_blocks(player_id)
    WHERE status = 'active';

CREATE INDEX training_blocks_player_status
    ON training_blocks(player_id, status, start_date DESC);

CREATE TABLE training_targets (
    id                  INTEGER PRIMARY KEY,
    block_id            INTEGER NOT NULL REFERENCES training_blocks(id) ON DELETE CASCADE,
    target_type         TEXT NOT NULL CHECK (target_type IN ('automatic', 'manual')),
    label               TEXT NOT NULL CHECK (length(trim(label)) > 0),
    metric_key          TEXT NOT NULL DEFAULT '',
    manual_prompt       TEXT NOT NULL DEFAULT '',
    comparator          TEXT NOT NULL CHECK (comparator IN ('<', '<=', '>', '>=', '=', 'yes', 'at_least')),
    threshold           REAL,
    unit                TEXT NOT NULL DEFAULT '',
    aggregation         TEXT NOT NULL DEFAULT 'per_game'
                            CHECK (aggregation IN ('per_game', 'rolling_average', 'success_rate')),
    window_games        INTEGER NOT NULL DEFAULT 1 CHECK (window_games >= 1),
    display_order       INTEGER NOT NULL DEFAULT 0 CHECK (display_order >= 0),
    CHECK (
        (target_type = 'automatic' AND metric_key <> '' AND threshold IS NOT NULL)
        OR
        (target_type = 'manual' AND manual_prompt <> '')
    )
);

CREATE INDEX training_targets_block_order
    ON training_targets(block_id, display_order, id);

CREATE TRIGGER training_targets_insert_guard
BEFORE INSERT ON training_targets
WHEN (SELECT status FROM training_blocks WHERE id = NEW.block_id) <> 'planned'
BEGIN
    SELECT RAISE(ABORT, 'targets are locked after block activation');
END;

CREATE TRIGGER training_targets_update_guard
BEFORE UPDATE ON training_targets
WHEN (SELECT status FROM training_blocks WHERE id = OLD.block_id) <> 'planned'
BEGIN
    SELECT RAISE(ABORT, 'targets are locked after block activation');
END;

CREATE TRIGGER training_targets_delete_guard
BEFORE DELETE ON training_targets
WHEN (SELECT status FROM training_blocks WHERE id = OLD.block_id) <> 'planned'
BEGIN
    SELECT RAISE(ABORT, 'targets are locked after block activation');
END;

CREATE TABLE match_training_blocks (
    match_id            INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    player_id           INTEGER NOT NULL REFERENCES player_profiles(id) ON DELETE CASCADE,
    block_id            INTEGER NOT NULL REFERENCES training_blocks(id) ON DELETE RESTRICT,
    assignment_source   TEXT NOT NULL CHECK (assignment_source IN ('time', 'manual', 'demo')),
    assigned_at         INTEGER NOT NULL,
    PRIMARY KEY (match_id, player_id)
);

CREATE INDEX match_training_blocks_block
    ON match_training_blocks(block_id, match_id);

CREATE TABLE match_reviews (
    id                  INTEGER PRIMARY KEY,
    match_id            INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    player_id           INTEGER NOT NULL REFERENCES player_profiles(id) ON DELETE CASCADE,
    grade_scale         TEXT CHECK (grade_scale IS NULL OR grade_scale IN ('numeric', 'letter')),
    grade_value         TEXT,
    grade_normalized    REAL CHECK (grade_normalized IS NULL OR grade_normalized BETWEEN 1 AND 5),
    biggest_mistake     TEXT NOT NULL DEFAULT '',
    done_well           TEXT NOT NULL DEFAULT '',
    next_game           TEXT NOT NULL DEFAULT '',
    drafted_at          INTEGER NOT NULL,
    completed_at        INTEGER,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    UNIQUE (match_id, player_id)
);

CREATE INDEX match_reviews_player_completion
    ON match_reviews(player_id, completed_at, match_id);

CREATE TABLE mistake_categories (
    id                  INTEGER PRIMARY KEY,
    slug                TEXT NOT NULL UNIQUE,
    label               TEXT NOT NULL,
    is_active           INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    is_custom           INTEGER NOT NULL DEFAULT 0 CHECK (is_custom IN (0, 1)),
    created_at          INTEGER NOT NULL
);

CREATE TABLE review_annotations (
    id                      INTEGER PRIMARY KEY,
    review_id               INTEGER NOT NULL REFERENCES match_reviews(id) ON DELETE CASCADE,
    category_id             INTEGER NOT NULL REFERENCES mistake_categories(id) ON DELETE RESTRICT,
    event_timestamp_seconds INTEGER CHECK (event_timestamp_seconds IS NULL OR event_timestamp_seconds >= 0),
    death_sequence          INTEGER CHECK (death_sequence IS NULL OR death_sequence >= 1),
    note                    TEXT NOT NULL DEFAULT '',
    created_at              INTEGER NOT NULL
);

CREATE INDEX review_annotations_review
    ON review_annotations(review_id, id);

CREATE INDEX review_annotations_category
    ON review_annotations(category_id, review_id);

CREATE UNIQUE INDEX review_annotation_whole_match_unique
    ON review_annotations(review_id, category_id)
    WHERE event_timestamp_seconds IS NULL AND death_sequence IS NULL;

CREATE TABLE target_checkins (
    target_id           INTEGER NOT NULL REFERENCES training_targets(id) ON DELETE CASCADE,
    match_id            INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    review_id           INTEGER REFERENCES match_reviews(id) ON DELETE CASCADE,
    boolean_value       INTEGER CHECK (boolean_value IS NULL OR boolean_value IN (0, 1)),
    rating_value        INTEGER CHECK (rating_value IS NULL OR rating_value BETWEEN 1 AND 5),
    note                TEXT NOT NULL DEFAULT '',
    source              TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'import')),
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    PRIMARY KEY (target_id, match_id),
    CHECK (boolean_value IS NOT NULL OR rating_value IS NOT NULL)
);

CREATE TABLE target_results (
    id                  INTEGER PRIMARY KEY,
    target_id           INTEGER NOT NULL REFERENCES training_targets(id) ON DELETE CASCADE,
    match_id            INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    actual_value        REAL,
    result_state        TEXT NOT NULL
                            CHECK (result_state IN ('met', 'missed', 'unknown', 'not_applicable')),
    evaluator_version   INTEGER NOT NULL CHECK (evaluator_version >= 1),
    is_current          INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
    evaluated_at        INTEGER NOT NULL,
    UNIQUE (target_id, match_id, evaluator_version)
);

CREATE UNIQUE INDEX one_current_target_result
    ON target_results(target_id, match_id)
    WHERE is_current = 1;

CREATE TABLE timeline_events (
    id                  INTEGER PRIMARY KEY,
    match_id            INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    sequence_number     INTEGER NOT NULL,
    timestamp_ms        INTEGER NOT NULL CHECK (timestamp_ms >= 0),
    event_type          TEXT NOT NULL,
    actor_participant_id INTEGER,
    victim_participant_id INTEGER,
    team_id             INTEGER,
    position_x          INTEGER,
    position_y          INTEGER,
    data_json           TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(data_json)),
    UNIQUE (match_id, sequence_number)
);

CREATE INDEX timeline_events_match_time
    ON timeline_events(match_id, timestamp_ms);

CREATE INDEX timeline_events_match_type
    ON timeline_events(match_id, event_type);

INSERT INTO mistake_categories (slug, label, is_active, is_custom, created_at) VALUES
    ('greed', 'Greed', 1, 0, unixepoch()),
    ('positioning', 'Positioning', 1, 0, unixepoch()),
    ('no-vision', 'No vision', 1, 0, unixepoch()),
    ('facecheck', 'Facecheck', 1, 0, unixepoch()),
    ('mechanical-error', 'Mechanical error', 1, 0, unixepoch()),
    ('matchup-knowledge', 'Matchup knowledge', 1, 0, unixepoch()),
    ('jungle-tracking', 'Jungle tracking', 1, 0, unixepoch()),
    ('bad-engage', 'Bad engage', 1, 0, unixepoch()),
    ('late-to-objective', 'Late to objective', 1, 0, unixepoch());
