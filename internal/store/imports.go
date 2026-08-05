package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	SyncTriggerStartup = "startup"
	SyncTriggerManual  = "manual"
	SyncTriggerPoll    = "poll"

	SyncStateRunning   = "running"
	SyncStateSucceeded = "succeeded"
	SyncStatePartial   = "partial"
	SyncStateFailed    = "failed"

	ImportJobPending          = "pending"
	ImportJobFetchingDetail   = "fetching_detail"
	ImportJobDetailStored     = "detail_stored"
	ImportJobDetailOnly       = "detail_only"
	ImportJobFetchingTimeline = "fetching_timeline"
	ImportJobComplete         = "complete"
	ImportJobPartialTimeline  = "partial_timeline"
	ImportJobRetryWait        = "retry_wait"
	ImportJobFailed           = "failed"

	ImportResumeDetail            = "detail"
	ImportResumeNormalizeDetail   = "normalize_detail"
	ImportResumeTimeline          = "timeline"
	ImportResumeNormalizeTimeline = "normalize_timeline"
	ImportResumeDone              = "done"

	PayloadKindMatch    = "match"
	PayloadKindTimeline = "timeline"

	MatchCompletenessDetailOnly      = "detail_only"
	MatchCompletenessComplete        = "complete"
	MatchCompletenessPartialTimeline = "partial_timeline"
)

const (
	maxStoredErrorCodeRunes    = 64
	maxStoredErrorMessageRunes = 1000
	defaultReadyJobLimit       = 50
	maxReadyJobLimit           = 200
)

var riotKeyPattern = regexp.MustCompile(`(?i)RGAPI-[A-Za-z0-9_-]+`)

// SyncRun records one bounded discovery/import attempt. A nil PlayerID is
// allowed for startup failures that happen before an account is resolved.
type SyncRun struct {
	ID              int64
	PlayerID        *int64
	Trigger         string
	State           string
	DiscoveredCount int
	ImportedCount   int
	SkippedCount    int
	FailedCount     int
	ErrorCode       string
	ErrorMessage    string
	StartedAt       time.Time
	CompletedAt     *time.Time
}

// SyncRunStart starts one sync attempt. StartedAt defaults to the current UTC
// time. PlayerID may be zero while first-time account resolution is in flight.
type SyncRunStart struct {
	PlayerID  int64
	Trigger   string
	StartedAt time.Time
}

// SyncRunFinish supplies the terminal result and sanitized display-safe error.
// The store additionally redacts Riot-looking keys and control characters.
type SyncRunFinish struct {
	State           string
	DiscoveredCount int
	ImportedCount   int
	SkippedCount    int
	FailedCount     int
	ErrorCode       string
	ErrorMessage    string
	CompletedAt     time.Time
}

// ImportJob is the durable resume point for one player and Riot match.
type ImportJob struct {
	ID            int64
	PlayerID      int64
	RiotMatchID   string
	LastSyncRunID *int64
	State         string
	ResumeStep    string
	AttemptCount  int
	NextAttemptAt *time.Time
	ErrorCode     string
	ErrorMessage  string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ImportJobStart durably queues a discovered match. Rediscovery updates the
// last sync association but deliberately preserves progress and attempt count.
type ImportJobStart struct {
	PlayerID    int64
	RiotMatchID string
	SyncRunID   int64
	QueuedAt    time.Time
}

// ImportJobUpdate advances a job. A nil NextAttemptAt clears any retry time.
// IncrementAttempt is useful immediately before an outbound fetch.
type ImportJobUpdate struct {
	JobID            int64
	State            string
	ResumeStep       string
	IncrementAttempt bool
	NextAttemptAt    *time.Time
	ErrorCode        string
	ErrorMessage     string
	UpdatedAt        time.Time
}

// APIPayload is one immutable raw response revision. Body contains the exact
// decompressed response bytes; SQLite keeps it gzip-compressed.
type APIPayload struct {
	ID          int64
	ImportJobID int64
	PlayerID    int64
	RiotMatchID string
	Kind        string
	Revision    int
	SHA256      string
	Body        []byte
	HTTPStatus  int
	FetchedAt   time.Time
	IsCurrent   bool
}

// APIPayloadInput stores raw response bytes before normalization.
type APIPayloadInput struct {
	PlayerID    int64
	RiotMatchID string
	Kind        string
	Body        []byte
	HTTPStatus  int
	FetchedAt   time.Time
}

// ImportedMatchInput is the persistence boundary for normalized Riot data.
// Set ReplaceTimeline for a successful timeline normalization, including a
// valid empty selected-event set. Detail-only writes leave prior events intact.
type ImportedMatchInput struct {
	PlayerID          int64
	RiotMatchID       string
	QueueID           int
	QueueType         string
	MapID             int
	GameMode          string
	GameType          string
	Patch             string
	GameStartAt       time.Time
	GameEndAt         time.Time
	DurationSeconds   int
	IsRemake          bool
	Surrendered       bool
	Completeness      string
	NormalizerVersion int
	ImportedAt        time.Time
	TrainingLocation  *time.Location
	Stats             ImportedPlayerStats
	TimelineEvents    []ImportedTimelineEvent
	ReplaceTimeline   bool
}

// ImportedPlayerStats is the primary player's normalized participant row.
type ImportedPlayerStats struct {
	ParticipantID     int
	TeamID            int
	ChampionID        int
	ChampionName      string
	Role              string
	Win               bool
	Kills             int
	Deaths            int
	Assists           int
	LaneMinions       int
	NeutralMinions    int
	Gold              int
	ChampionDamage    int
	VisionScore       int
	WardsPlaced       int
	WardsKilled       int
	VisionWardsBought int
	OpponentChampion  string
	FinalItems        []int
	Runes             []int
	SummonerSpells    []int
	SkillOrder        []int
}

// ImportedTimelineEvent is one selected, normalized event. SequenceNumber must
// be stable for a given raw timeline so replacement is deterministic.
type ImportedTimelineEvent struct {
	SequenceNumber      int
	TimestampMS         int64
	EventType           string
	ActorParticipantID  *int
	VictimParticipantID *int
	TeamID              *int
	PositionX           *int
	PositionY           *int
	DataJSON            json.RawMessage
}

// StartSyncRun creates a running sync record.
func (s *Store) StartSyncRun(ctx context.Context, input SyncRunStart) (*SyncRun, error) {
	input.Trigger = strings.TrimSpace(input.Trigger)
	if !validSyncTrigger(input.Trigger) {
		return nil, fmt.Errorf("%w: invalid sync trigger", ErrInvalidInput)
	}
	if input.PlayerID < 0 {
		return nil, fmt.Errorf("%w: player ID cannot be negative", ErrInvalidInput)
	}
	startedAt := input.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_runs (
			player_id, trigger_source, state, started_at
		) VALUES (?, ?, 'running', ?)
	`, nullablePositiveID(input.PlayerID), input.Trigger, startedAt.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("start sync run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read sync run ID: %w", err)
	}
	return s.syncRun(ctx, id)
}

// FinishSyncRun records a terminal outcome.
func (s *Store) FinishSyncRun(ctx context.Context, id int64, input SyncRunFinish) error {
	if id <= 0 {
		return fmt.Errorf("%w: sync run ID is required", ErrInvalidInput)
	}
	input.State = strings.TrimSpace(input.State)
	if input.State != SyncStateSucceeded &&
		input.State != SyncStatePartial &&
		input.State != SyncStateFailed {
		return fmt.Errorf("%w: sync run needs a terminal state", ErrInvalidInput)
	}
	if input.DiscoveredCount < 0 || input.ImportedCount < 0 ||
		input.SkippedCount < 0 || input.FailedCount < 0 {
		return fmt.Errorf("%w: sync counts cannot be negative", ErrInvalidInput)
	}
	completedAt := input.CompletedAt
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	code, message := safeStoredError(input.ErrorCode, input.ErrorMessage)
	if input.State == SyncStateSucceeded {
		code, message = "", ""
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE sync_runs
		SET state = ?, discovered_count = ?, imported_count = ?,
		    skipped_count = ?, failed_count = ?, error_code = ?,
		    error_message = ?, completed_at = ?
		WHERE id = ? AND state = 'running'
	`, input.State, input.DiscoveredCount, input.ImportedCount,
		input.SkippedCount, input.FailedCount, code, message,
		completedAt.UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("finish sync run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read finished sync run count: %w", err)
	}
	if affected == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM sync_runs WHERE id = ?)", id,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check sync run: %w", err)
		}
		if exists == 0 {
			return ErrNotFound
		}
		return fmt.Errorf("%w: sync run is already finished", ErrInvalidInput)
	}
	return nil
}

// LatestSyncRun returns the newest run for a player.
func (s *Store) LatestSyncRun(ctx context.Context, playerID int64) (*SyncRun, error) {
	if playerID <= 0 {
		return nil, fmt.Errorf("%w: player ID is required", ErrInvalidInput)
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, player_id, trigger_source, state, discovered_count,
		       imported_count, skipped_count, failed_count, error_code,
		       error_message, started_at, completed_at
		FROM sync_runs
		WHERE player_id = ?
		ORDER BY started_at DESC, id DESC
		LIMIT 1
	`, playerID)
	run, err := scanSyncRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get latest sync run: %w", err)
	}
	return &run, nil
}

func (s *Store) syncRun(ctx context.Context, id int64) (*SyncRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, player_id, trigger_source, state, discovered_count,
		       imported_count, skipped_count, failed_count, error_code,
		       error_message, started_at, completed_at
		FROM sync_runs
		WHERE id = ?
	`, id)
	run, err := scanSyncRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sync run: %w", err)
	}
	return &run, nil
}

func scanSyncRun(row rowScanner) (SyncRun, error) {
	var run SyncRun
	var playerID, completedAt sql.NullInt64
	var startedAt int64
	err := row.Scan(
		&run.ID, &playerID, &run.Trigger, &run.State, &run.DiscoveredCount,
		&run.ImportedCount, &run.SkippedCount, &run.FailedCount,
		&run.ErrorCode, &run.ErrorMessage, &startedAt, &completedAt,
	)
	if playerID.Valid {
		value := playerID.Int64
		run.PlayerID = &value
	}
	run.StartedAt = unixTime(startedAt)
	if completedAt.Valid {
		value := unixTime(completedAt.Int64)
		run.CompletedAt = &value
	}
	return run, err
}

// QueueImportJob creates a durable job or returns its current state when the
// player/match pair was already discovered.
func (s *Store) QueueImportJob(ctx context.Context, input ImportJobStart) (*ImportJob, error) {
	input.RiotMatchID = strings.TrimSpace(input.RiotMatchID)
	if input.PlayerID <= 0 || input.RiotMatchID == "" {
		return nil, fmt.Errorf("%w: player ID and Riot match ID are required", ErrInvalidInput)
	}
	if input.SyncRunID < 0 {
		return nil, fmt.Errorf("%w: sync run ID cannot be negative", ErrInvalidInput)
	}
	queuedAt := input.QueuedAt
	if queuedAt.IsZero() {
		queuedAt = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin queue import job: %w", err)
	}
	defer tx.Rollback()

	if input.SyncRunID != 0 {
		var compatible int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM sync_runs
				WHERE id = ? AND (player_id IS NULL OR player_id = ?)
			)
		`, input.SyncRunID, input.PlayerID).Scan(&compatible); err != nil {
			return nil, fmt.Errorf("check import job sync run: %w", err)
		}
		if compatible == 0 {
			return nil, fmt.Errorf("%w: sync run does not belong to player", ErrInvalidInput)
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO import_jobs (
			player_id, riot_match_id, last_sync_run_id, state, resume_step,
			attempt_count, created_at, updated_at
		) VALUES (?, ?, ?, 'pending', 'detail', 0, ?, ?)
		ON CONFLICT(player_id, riot_match_id) DO UPDATE SET
			last_sync_run_id = COALESCE(
				excluded.last_sync_run_id,
				import_jobs.last_sync_run_id
			),
			updated_at = excluded.updated_at
	`, input.PlayerID, input.RiotMatchID, nullablePositiveID(input.SyncRunID),
		queuedAt.UTC().Unix(), queuedAt.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("queue import job: %w", err)
	}
	job, err := importJobByPlayerMatch(ctx, tx, input.PlayerID, input.RiotMatchID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit import job queue: %w", err)
	}
	return job, nil
}

// UpdateImportJob advances one job and returns its persisted state.
func (s *Store) UpdateImportJob(ctx context.Context, input ImportJobUpdate) (*ImportJob, error) {
	if input.JobID <= 0 || !validImportJobState(input.State) ||
		!validImportResumeStep(input.ResumeStep) {
		return nil, fmt.Errorf("%w: job ID, state, and resume step are required", ErrInvalidInput)
	}
	updatedAt := input.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	code, message := safeStoredError(input.ErrorCode, input.ErrorMessage)
	if input.State == ImportJobComplete {
		code, message = "", ""
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE import_jobs
		SET state = ?, resume_step = ?,
		    attempt_count = attempt_count + ?,
		    next_attempt_at = ?, error_code = ?, error_message = ?,
		    updated_at = ?
		WHERE id = ?
	`, input.State, input.ResumeStep, boolInt(input.IncrementAttempt),
		nullableTime(input.NextAttemptAt), code, message,
		updatedAt.UTC().Unix(), input.JobID)
	if err != nil {
		return nil, fmt.Errorf("update import job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read updated import job count: %w", err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return s.importJob(ctx, input.JobID)
}

// ReadyImportJobs returns resumable nonterminal jobs whose retry time has
// arrived. Failed jobs are terminal until explicitly advanced by the importer.
func (s *Store) ReadyImportJobs(
	ctx context.Context,
	playerID int64,
	limit int,
) ([]ImportJob, error) {
	if playerID <= 0 {
		return nil, fmt.Errorf("%w: player ID is required", ErrInvalidInput)
	}
	if limit <= 0 {
		limit = defaultReadyJobLimit
	}
	if limit > maxReadyJobLimit {
		limit = maxReadyJobLimit
	}
	now := time.Now().UTC().Unix()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, player_id, riot_match_id, last_sync_run_id, state,
		       resume_step, attempt_count, next_attempt_at, error_code,
		       error_message, created_at, updated_at
		FROM import_jobs
		WHERE player_id = ?
		  AND state NOT IN ('complete', 'failed')
		  AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY created_at, id
		LIMIT ?
	`, playerID, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list ready import jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]ImportJob, 0)
	for rows.Next() {
		job, err := scanImportJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ready import job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ready import jobs: %w", err)
	}
	return jobs, nil
}

func (s *Store) importJob(ctx context.Context, id int64) (*ImportJob, error) {
	job, err := scanImportJob(s.db.QueryRowContext(ctx, `
		SELECT id, player_id, riot_match_id, last_sync_run_id, state,
		       resume_step, attempt_count, next_attempt_at, error_code,
		       error_message, created_at, updated_at
		FROM import_jobs
		WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get import job: %w", err)
	}
	return &job, nil
}

func importJobByPlayerMatch(
	ctx context.Context,
	queryer rowQueryer,
	playerID int64,
	riotMatchID string,
) (*ImportJob, error) {
	job, err := scanImportJob(queryer.QueryRowContext(ctx, `
		SELECT id, player_id, riot_match_id, last_sync_run_id, state,
		       resume_step, attempt_count, next_attempt_at, error_code,
		       error_message, created_at, updated_at
		FROM import_jobs
		WHERE player_id = ? AND riot_match_id = ?
	`, playerID, riotMatchID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get import job by match: %w", err)
	}
	return &job, nil
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanImportJob(row rowScanner) (ImportJob, error) {
	var job ImportJob
	var syncRunID, nextAttemptAt sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(
		&job.ID, &job.PlayerID, &job.RiotMatchID, &syncRunID, &job.State,
		&job.ResumeStep, &job.AttemptCount, &nextAttemptAt, &job.ErrorCode,
		&job.ErrorMessage, &createdAt, &updatedAt,
	)
	if syncRunID.Valid {
		value := syncRunID.Int64
		job.LastSyncRunID = &value
	}
	if nextAttemptAt.Valid {
		value := unixTime(nextAttemptAt.Int64)
		job.NextAttemptAt = &value
	}
	job.CreatedAt = unixTime(createdAt)
	job.UpdatedAt = unixTime(updatedAt)
	return job, err
}

// SaveAPIPayload gzip-compresses and saves a raw response before normalization.
// An identical body reuses its SHA-identified revision; a changed body creates
// the next revision and becomes current.
func (s *Store) SaveAPIPayload(
	ctx context.Context,
	input APIPayloadInput,
) (*APIPayload, error) {
	input.RiotMatchID = strings.TrimSpace(input.RiotMatchID)
	input.Kind = strings.TrimSpace(input.Kind)
	if input.PlayerID <= 0 || input.RiotMatchID == "" ||
		!validPayloadKind(input.Kind) || len(input.Body) == 0 ||
		input.HTTPStatus < 100 || input.HTTPStatus > 599 {
		return nil, fmt.Errorf("%w: valid job, payload kind, body, and HTTP status are required", ErrInvalidInput)
	}
	fetchedAt := input.FetchedAt
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	digest := sha256.Sum256(input.Body)
	hash := hex.EncodeToString(digest[:])
	compressed, err := gzipPayload(input.Body)
	if err != nil {
		return nil, fmt.Errorf("compress API payload: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin save API payload: %w", err)
	}
	defer tx.Rollback()

	job, err := importJobByPlayerMatch(ctx, tx, input.PlayerID, input.RiotMatchID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: queue the import job before storing payloads", ErrInvalidInput)
		}
		return nil, err
	}

	var payloadID int64
	var revision int
	err = tx.QueryRowContext(ctx, `
		SELECT id, revision
		FROM api_payloads
		WHERE import_job_id = ? AND payload_kind = ? AND sha256 = ?
	`, job.ID, input.Kind, hash).Scan(&payloadID, &revision)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `
			UPDATE api_payloads
			SET is_current = 0
			WHERE import_job_id = ? AND payload_kind = ? AND is_current = 1
		`, job.ID, input.Kind); err != nil {
			return nil, fmt.Errorf("clear current API payload: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE api_payloads
			SET is_current = 1, http_status = ?, fetched_at = ?
			WHERE id = ?
		`, input.HTTPStatus, fetchedAt.UTC().Unix(), payloadID); err != nil {
			return nil, fmt.Errorf("reactivate API payload revision: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(revision), 0) + 1
			FROM api_payloads
			WHERE import_job_id = ? AND payload_kind = ?
		`, job.ID, input.Kind).Scan(&revision); err != nil {
			return nil, fmt.Errorf("choose API payload revision: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE api_payloads
			SET is_current = 0
			WHERE import_job_id = ? AND payload_kind = ? AND is_current = 1
		`, job.ID, input.Kind); err != nil {
			return nil, fmt.Errorf("clear current API payload: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO api_payloads (
				import_job_id, payload_kind, revision, sha256,
				content_encoding, payload, http_status, fetched_at, is_current
			) VALUES (?, ?, ?, ?, 'gzip', ?, ?, ?, 1)
		`, job.ID, input.Kind, revision, hash, compressed, input.HTTPStatus,
			fetchedAt.UTC().Unix())
		if err != nil {
			return nil, fmt.Errorf("insert API payload revision: %w", err)
		}
		payloadID, err = result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("read API payload ID: %w", err)
		}
	default:
		return nil, fmt.Errorf("find matching API payload revision: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit API payload: %w", err)
	}
	return s.apiPayload(ctx, payloadID)
}

// CurrentAPIPayload returns the current raw response for re-normalization.
func (s *Store) CurrentAPIPayload(
	ctx context.Context,
	playerID int64,
	riotMatchID string,
	kind string,
) (*APIPayload, error) {
	riotMatchID = strings.TrimSpace(riotMatchID)
	kind = strings.TrimSpace(kind)
	if playerID <= 0 || riotMatchID == "" || !validPayloadKind(kind) {
		return nil, fmt.Errorf("%w: player, match, and payload kind are required", ErrInvalidInput)
	}
	return scanAPIPayload(s.db.QueryRowContext(ctx, `
		SELECT ap.id, ap.import_job_id, ij.player_id, ij.riot_match_id,
		       ap.payload_kind, ap.revision, ap.sha256, ap.payload,
		       ap.http_status, ap.fetched_at, ap.is_current
		FROM api_payloads ap
		JOIN import_jobs ij ON ij.id = ap.import_job_id
		WHERE ij.player_id = ?
		  AND ij.riot_match_id = ?
		  AND ap.payload_kind = ?
		  AND ap.is_current = 1
	`, playerID, riotMatchID, kind))
}

func (s *Store) apiPayload(ctx context.Context, id int64) (*APIPayload, error) {
	return scanAPIPayload(s.db.QueryRowContext(ctx, `
		SELECT ap.id, ap.import_job_id, ij.player_id, ij.riot_match_id,
		       ap.payload_kind, ap.revision, ap.sha256, ap.payload,
		       ap.http_status, ap.fetched_at, ap.is_current
		FROM api_payloads ap
		JOIN import_jobs ij ON ij.id = ap.import_job_id
		WHERE ap.id = ?
	`, id))
}

func scanAPIPayload(row rowScanner) (*APIPayload, error) {
	var payload APIPayload
	var compressed []byte
	var fetchedAt int64
	var current int
	err := row.Scan(
		&payload.ID, &payload.ImportJobID, &payload.PlayerID,
		&payload.RiotMatchID, &payload.Kind, &payload.Revision,
		&payload.SHA256, &compressed, &payload.HTTPStatus, &fetchedAt, &current,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan API payload: %w", err)
	}
	payload.Body, err = gunzipPayload(compressed)
	if err != nil {
		return nil, fmt.Errorf("decompress API payload %d: %w", payload.ID, err)
	}
	payload.FetchedAt = unixTime(fetchedAt)
	payload.IsCurrent = current == 1
	return &payload, nil
}

// UpsertImportedMatch atomically replaces imported match/player data and, when
// requested, selected timeline events. Review and training rows are never
// deleted. An unassigned match is attached to the active or completed training
// block whose configured local-date bounds contain GameStartAt, then automatic
// results are recomputed.
func (s *Store) UpsertImportedMatch(
	ctx context.Context,
	input ImportedMatchInput,
) (int64, error) {
	prepared, err := prepareImportedMatch(input)
	if err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin imported match upsert: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	importedAt := input.ImportedAt
	if importedAt.IsZero() {
		importedAt = now
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO matches (
			riot_match_id, queue_id, queue_type, map_id, game_mode, game_type,
			patch, game_start_at, game_end_at, duration_seconds, is_remake,
			surrendered, completeness, normalizer_version, imported_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(riot_match_id) DO UPDATE SET
			queue_id = excluded.queue_id,
			queue_type = excluded.queue_type,
			map_id = excluded.map_id,
			game_mode = excluded.game_mode,
			game_type = excluded.game_type,
			patch = excluded.patch,
			game_start_at = excluded.game_start_at,
			game_end_at = excluded.game_end_at,
			duration_seconds = excluded.duration_seconds,
			is_remake = excluded.is_remake,
			surrendered = excluded.surrendered,
			completeness = CASE
				WHEN matches.completeness = 'complete'
				 AND excluded.completeness <> 'complete'
				THEN matches.completeness
				ELSE excluded.completeness
			END,
			normalizer_version = excluded.normalizer_version,
			updated_at = excluded.updated_at
	`, prepared.RiotMatchID, prepared.QueueID, prepared.QueueType,
		prepared.MapID, prepared.GameMode, prepared.GameType, prepared.Patch,
		prepared.GameStartAt.UTC().Unix(), prepared.GameEndAt.UTC().Unix(),
		prepared.DurationSeconds, boolInt(prepared.IsRemake),
		boolInt(prepared.Surrendered), prepared.Completeness,
		prepared.NormalizerVersion, importedAt.UTC().Unix(), now.Unix())
	if err != nil {
		return 0, fmt.Errorf("upsert imported match: %w", err)
	}

	var matchID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM matches WHERE riot_match_id = ?
	`, prepared.RiotMatchID).Scan(&matchID); err != nil {
		return 0, fmt.Errorf("read imported match ID: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO player_match_stats (
			match_id, player_id, participant_id, team_id, champion_id,
			champion_name, role, win, kills, deaths, assists, lane_minions,
			neutral_minions, gold, champion_damage, vision_score, wards_placed,
			wards_killed, vision_wards_bought, opponent_champion,
			final_items_json, runes_json, summoner_spells_json, skill_order_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id, player_id) DO UPDATE SET
			participant_id = excluded.participant_id,
			team_id = excluded.team_id,
			champion_id = excluded.champion_id,
			champion_name = excluded.champion_name,
			role = excluded.role,
			win = excluded.win,
			kills = excluded.kills,
			deaths = excluded.deaths,
			assists = excluded.assists,
			lane_minions = excluded.lane_minions,
			neutral_minions = excluded.neutral_minions,
			gold = excluded.gold,
			champion_damage = excluded.champion_damage,
			vision_score = excluded.vision_score,
			wards_placed = excluded.wards_placed,
			wards_killed = excluded.wards_killed,
			vision_wards_bought = excluded.vision_wards_bought,
			opponent_champion = excluded.opponent_champion,
			final_items_json = excluded.final_items_json,
			runes_json = excluded.runes_json,
			summoner_spells_json = excluded.summoner_spells_json,
			skill_order_json = CASE
				WHEN ? = 1 THEN excluded.skill_order_json
				ELSE player_match_stats.skill_order_json
			END
	`, matchID, prepared.PlayerID, prepared.Stats.ParticipantID,
		prepared.Stats.TeamID, prepared.Stats.ChampionID,
		prepared.Stats.ChampionName, prepared.Stats.Role,
		boolInt(prepared.Stats.Win), prepared.Stats.Kills,
		prepared.Stats.Deaths, prepared.Stats.Assists,
		prepared.Stats.LaneMinions, prepared.Stats.NeutralMinions,
		prepared.Stats.Gold, prepared.Stats.ChampionDamage,
		prepared.Stats.VisionScore, prepared.Stats.WardsPlaced,
		prepared.Stats.WardsKilled, prepared.Stats.VisionWardsBought,
		prepared.Stats.OpponentChampion, prepared.finalItemsJSON,
		prepared.runesJSON, prepared.summonerSpellsJSON,
		prepared.skillOrderJSON, boolInt(prepared.ReplaceTimeline))
	if err != nil {
		return 0, fmt.Errorf("upsert imported player stats: %w", err)
	}

	if prepared.ReplaceTimeline {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM timeline_events WHERE match_id = ?", matchID,
		); err != nil {
			return 0, fmt.Errorf("replace imported timeline events: %w", err)
		}
		for _, event := range prepared.TimelineEvents {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO timeline_events (
					match_id, sequence_number, timestamp_ms, event_type,
					actor_participant_id, victim_participant_id, team_id,
					position_x, position_y, data_json
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, matchID, event.SequenceNumber, event.TimestampMS,
				event.EventType, event.ActorParticipantID,
				event.VictimParticipantID, event.TeamID, event.PositionX,
				event.PositionY, string(event.DataJSON)); err != nil {
				return 0, fmt.Errorf(
					"insert imported timeline event %d: %w",
					event.SequenceNumber,
					err,
				)
			}
		}
	}

	blockID, err := assignImportedMatchTx(
		ctx,
		tx,
		matchID,
		prepared.PlayerID,
		prepared.GameStartAt,
		prepared.TrainingLocation,
		now,
	)
	if err != nil {
		return 0, err
	}
	if blockID != 0 {
		if err := recomputeBlockTargetResults(ctx, tx, blockID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit imported match upsert: %w", err)
	}
	return matchID, nil
}

// MarkMatchTimelinePartial records an unavailable timeline without discarding a
// previously complete timeline or any selected events.
func (s *Store) MarkMatchTimelinePartial(
	ctx context.Context,
	playerID int64,
	riotMatchID string,
	normalizerVersion int,
) error {
	riotMatchID = strings.TrimSpace(riotMatchID)
	if playerID <= 0 || riotMatchID == "" {
		return fmt.Errorf("%w: player and Riot match ID are required", ErrInvalidInput)
	}
	if normalizerVersion < 1 {
		normalizerVersion = 1
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE matches
		SET completeness = CASE
				WHEN completeness = 'complete' THEN completeness
				ELSE 'partial_timeline'
			END,
		    normalizer_version = ?,
		    updated_at = ?
		WHERE riot_match_id = ?
		  AND EXISTS (
		      SELECT 1 FROM player_match_stats pms
		      WHERE pms.match_id = matches.id AND pms.player_id = ?
		  )
	`, normalizerVersion, time.Now().UTC().Unix(), riotMatchID, playerID)
	if err != nil {
		return fmt.Errorf("mark match timeline partial: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read partial timeline match count: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

type preparedImportedMatch struct {
	ImportedMatchInput
	finalItemsJSON     string
	runesJSON          string
	summonerSpellsJSON string
	skillOrderJSON     string
}

func prepareImportedMatch(input ImportedMatchInput) (preparedImportedMatch, error) {
	input.RiotMatchID = strings.TrimSpace(input.RiotMatchID)
	input.QueueType = strings.TrimSpace(input.QueueType)
	input.GameMode = strings.TrimSpace(input.GameMode)
	input.GameType = strings.TrimSpace(input.GameType)
	input.Patch = strings.TrimSpace(input.Patch)
	input.Completeness = strings.TrimSpace(input.Completeness)
	input.Stats.ChampionName = strings.TrimSpace(input.Stats.ChampionName)
	input.Stats.Role = strings.TrimSpace(input.Stats.Role)
	input.Stats.OpponentChampion = strings.TrimSpace(input.Stats.OpponentChampion)

	if input.PlayerID <= 0 || input.RiotMatchID == "" ||
		input.GameStartAt.IsZero() || input.Stats.ChampionName == "" {
		return preparedImportedMatch{}, fmt.Errorf(
			"%w: player, match, start time, and champion are required",
			ErrInvalidInput,
		)
	}
	if input.QueueID < 0 || input.MapID < 0 || input.DurationSeconds < 0 {
		return preparedImportedMatch{}, fmt.Errorf(
			"%w: queue, map, and duration cannot be negative",
			ErrInvalidInput,
		)
	}
	if input.GameEndAt.IsZero() {
		input.GameEndAt = input.GameStartAt.Add(
			time.Duration(input.DurationSeconds) * time.Second,
		)
	}
	if input.GameEndAt.Before(input.GameStartAt) {
		return preparedImportedMatch{}, fmt.Errorf(
			"%w: game end cannot precede game start",
			ErrInvalidInput,
		)
	}
	if input.QueueType == "" {
		input.QueueType = "Unknown"
	}
	if input.Stats.Role == "" {
		input.Stats.Role = "UNKNOWN"
	}
	if input.NormalizerVersion < 1 {
		input.NormalizerVersion = 1
	}
	if !validMatchCompleteness(input.Completeness) {
		return preparedImportedMatch{}, fmt.Errorf(
			"%w: invalid match completeness",
			ErrInvalidInput,
		)
	}
	if input.ReplaceTimeline && input.Completeness != MatchCompletenessComplete {
		return preparedImportedMatch{}, fmt.Errorf(
			"%w: timeline replacement requires complete normalization",
			ErrInvalidInput,
		)
	}
	if err := validateImportedStats(input.Stats); err != nil {
		return preparedImportedMatch{}, err
	}

	seenSequences := make(map[int]struct{}, len(input.TimelineEvents))
	for index := range input.TimelineEvents {
		event := &input.TimelineEvents[index]
		event.EventType = strings.TrimSpace(event.EventType)
		if event.SequenceNumber < 0 || event.TimestampMS < 0 ||
			event.EventType == "" {
			return preparedImportedMatch{}, fmt.Errorf(
				"%w: timeline event %d is invalid",
				ErrInvalidInput,
				index,
			)
		}
		if _, exists := seenSequences[event.SequenceNumber]; exists {
			return preparedImportedMatch{}, fmt.Errorf(
				"%w: duplicate timeline sequence %d",
				ErrInvalidInput,
				event.SequenceNumber,
			)
		}
		seenSequences[event.SequenceNumber] = struct{}{}
		if len(event.DataJSON) == 0 {
			event.DataJSON = json.RawMessage(`{}`)
		}
		if !json.Valid(event.DataJSON) {
			return preparedImportedMatch{}, fmt.Errorf(
				"%w: timeline event %d data is not JSON",
				ErrInvalidInput,
				event.SequenceNumber,
			)
		}
	}

	finalItemsJSON, err := marshalIntSlice(input.Stats.FinalItems)
	if err != nil {
		return preparedImportedMatch{}, fmt.Errorf("encode final items: %w", err)
	}
	runesJSON, err := marshalIntSlice(input.Stats.Runes)
	if err != nil {
		return preparedImportedMatch{}, fmt.Errorf("encode runes: %w", err)
	}
	summonerSpellsJSON, err := marshalIntSlice(input.Stats.SummonerSpells)
	if err != nil {
		return preparedImportedMatch{}, fmt.Errorf("encode summoner spells: %w", err)
	}
	skillOrderJSON, err := marshalIntSlice(input.Stats.SkillOrder)
	if err != nil {
		return preparedImportedMatch{}, fmt.Errorf("encode skill order: %w", err)
	}

	return preparedImportedMatch{
		ImportedMatchInput: input,
		finalItemsJSON:     finalItemsJSON,
		runesJSON:          runesJSON,
		summonerSpellsJSON: summonerSpellsJSON,
		skillOrderJSON:     skillOrderJSON,
	}, nil
}

func validateImportedStats(stats ImportedPlayerStats) error {
	values := []int{
		stats.ParticipantID,
		stats.TeamID,
		stats.ChampionID,
		stats.Kills,
		stats.Deaths,
		stats.Assists,
		stats.LaneMinions,
		stats.NeutralMinions,
		stats.Gold,
		stats.ChampionDamage,
		stats.VisionScore,
		stats.WardsPlaced,
		stats.WardsKilled,
		stats.VisionWardsBought,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf(
				"%w: imported participant values cannot be negative",
				ErrInvalidInput,
			)
		}
	}
	for _, values := range [][]int{
		stats.FinalItems,
		stats.Runes,
		stats.SummonerSpells,
		stats.SkillOrder,
	} {
		for _, value := range values {
			if value < 0 {
				return fmt.Errorf(
					"%w: imported IDs cannot be negative",
					ErrInvalidInput,
				)
			}
		}
	}
	return nil
}

func marshalIntSlice(values []int) (string, error) {
	if values == nil {
		values = []int{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func assignImportedMatchTx(
	ctx context.Context,
	tx *sql.Tx,
	matchID int64,
	playerID int64,
	gameStartAt time.Time,
	location *time.Location,
	assignedAt time.Time,
) (int64, error) {
	if location == nil {
		location = time.UTC
	}
	localGameDate := gameStartAt.In(location).Format(time.DateOnly)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO match_training_blocks (
			match_id, player_id, block_id, assignment_source, assigned_at
		)
		SELECT ?, ?, candidate.id, 'time', ?
		FROM (
			SELECT tb.id
			FROM training_blocks tb
			WHERE tb.player_id = ?
			  AND tb.status IN ('active', 'completed')
			  AND ? >= tb.start_date
			  AND (
			      tb.end_date IS NULL
			      OR ? <= tb.end_date
			  )
			ORDER BY
			  CASE tb.status WHEN 'active' THEN 0 ELSE 1 END,
			  tb.start_date DESC,
			  tb.id DESC
			LIMIT 1
		) AS candidate
		WHERE 1
		ON CONFLICT(match_id, player_id) DO NOTHING
	`, matchID, playerID, assignedAt.UTC().Unix(), playerID,
		localGameDate, localGameDate)
	if err != nil {
		return 0, fmt.Errorf("assign imported match to training block: %w", err)
	}

	var blockID int64
	err = tx.QueryRowContext(ctx, `
		SELECT block_id
		FROM match_training_blocks
		WHERE match_id = ? AND player_id = ?
	`, matchID, playerID).Scan(&blockID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read imported match training block: %w", err)
	}
	return blockID, nil
}

func validSyncTrigger(value string) bool {
	switch value {
	case SyncTriggerStartup, SyncTriggerManual, SyncTriggerPoll:
		return true
	default:
		return false
	}
}

func validImportJobState(value string) bool {
	switch value {
	case ImportJobPending,
		ImportJobFetchingDetail,
		ImportJobDetailStored,
		ImportJobDetailOnly,
		ImportJobFetchingTimeline,
		ImportJobComplete,
		ImportJobPartialTimeline,
		ImportJobRetryWait,
		ImportJobFailed:
		return true
	default:
		return false
	}
}

func validImportResumeStep(value string) bool {
	switch value {
	case ImportResumeDetail,
		ImportResumeNormalizeDetail,
		ImportResumeTimeline,
		ImportResumeNormalizeTimeline,
		ImportResumeDone:
		return true
	default:
		return false
	}
}

func validPayloadKind(value string) bool {
	return value == PayloadKindMatch || value == PayloadKindTimeline
}

func validMatchCompleteness(value string) bool {
	switch value {
	case MatchCompletenessDetailOnly,
		MatchCompletenessComplete,
		MatchCompletenessPartialTimeline:
		return true
	default:
		return false
	}
}

func nullablePositiveID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Unix()
}

func safeStoredError(code string, message string) (string, string) {
	code = normalizeStoredError(code)
	message = normalizeStoredError(message)
	code = truncateRunes(code, maxStoredErrorCodeRunes)
	message = truncateRunes(message, maxStoredErrorMessageRunes)
	return code, message
}

func normalizeStoredError(value string) string {
	value = riotKeyPattern.ReplaceAllString(value, "[REDACTED_RIOT_KEY]")
	value = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func truncateRunes(value string, limit int) string {
	if limit < 1 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func gzipPayload(body []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

func gunzipPayload(body []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return decompressed, nil
}
