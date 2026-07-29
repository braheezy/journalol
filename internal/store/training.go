package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"journalol/internal/model"
)

var automaticMetricKeys = map[string]struct{}{
	"deaths":            {},
	"kda":               {},
	"vision_per_minute": {},
	"control_wards":     {},
	"cs":                {},
	"gold":              {},
	"champion_damage":   {},
	"win":               {},
}

// CreateTrainingBlock creates an editable planned block and its targets.
func (s *Store) CreateTrainingBlock(ctx context.Context, params model.CreateTrainingBlockParams) (*model.TrainingBlock, error) {
	prepared, err := s.prepareTrainingBlockParams(ctx, params)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create training block: %w", err)
	}
	defer tx.Rollback()

	blockID, err := createTrainingBlockTx(ctx, tx, prepared, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit training block: %w", err)
	}
	return s.trainingBlock(ctx, blockID)
}

// CreateAndActivateTrainingBlock creates and activates a block in one
// transaction. If activation is rejected or fails, no planned block remains.
func (s *Store) CreateAndActivateTrainingBlock(
	ctx context.Context,
	params model.CreateTrainingBlockParams,
	replaceActive bool,
) (*model.TrainingBlock, error) {
	return s.CreateAndActivateTrainingBlockAt(
		ctx,
		params,
		replaceActive,
		time.Now(),
		time.UTC,
	)
}

// CreateAndActivateTrainingBlockAt is the location-aware variant used by the
// web application. Local calendar dates are interpreted in location, while now
// makes lifecycle decisions deterministic and testable.
func (s *Store) CreateAndActivateTrainingBlockAt(
	ctx context.Context,
	params model.CreateTrainingBlockParams,
	replaceActive bool,
	now time.Time,
	location *time.Location,
) (*model.TrainingBlock, error) {
	localNow, err := activationNow(now, location)
	if err != nil {
		return nil, err
	}
	if params.StartDate == "" {
		params.StartDate = localNow.Format(time.DateOnly)
	}
	prepared, err := s.prepareTrainingBlockParams(ctx, params)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create and activate training block: %w", err)
	}
	defer tx.Rollback()

	blockID, err := createTrainingBlockTx(ctx, tx, prepared, now)
	if err != nil {
		return nil, err
	}
	if err := activateTrainingBlockTx(ctx, tx, blockID, replaceActive, now, location); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit created active training block: %w", err)
	}
	return s.trainingBlock(ctx, blockID)
}

func (s *Store) prepareTrainingBlockParams(
	ctx context.Context,
	params model.CreateTrainingBlockParams,
) (model.CreateTrainingBlockParams, error) {
	params.Name = strings.TrimSpace(params.Name)
	params.Description = strings.TrimSpace(params.Description)
	params.Reminder = strings.TrimSpace(params.Reminder)
	params.Notes = strings.TrimSpace(params.Notes)
	if params.Name == "" {
		return model.CreateTrainingBlockParams{}, fmt.Errorf("%w: training block name is required", ErrInvalidInput)
	}
	if params.PlayerID == 0 {
		player, err := s.PrimaryPlayer(ctx)
		if err != nil {
			return model.CreateTrainingBlockParams{}, err
		}
		params.PlayerID = player.ID
	}
	if params.StartDate == "" {
		params.StartDate = time.Now().UTC().Format(time.DateOnly)
	}
	if err := validateDate(params.StartDate); err != nil {
		return model.CreateTrainingBlockParams{}, err
	}
	if params.EndDate != nil {
		trimmed := strings.TrimSpace(*params.EndDate)
		if trimmed == "" {
			params.EndDate = nil
		} else {
			if err := validateDate(trimmed); err != nil {
				return model.CreateTrainingBlockParams{}, err
			}
			if trimmed < params.StartDate {
				return model.CreateTrainingBlockParams{}, fmt.Errorf("%w: end date precedes start date", ErrInvalidInput)
			}
			params.EndDate = &trimmed
		}
	}
	for i := range params.Targets {
		if err := validateTarget(&params.Targets[i]); err != nil {
			return model.CreateTrainingBlockParams{}, fmt.Errorf("target %d: %w", i+1, err)
		}
	}
	return params, nil
}

func createTrainingBlockTx(
	ctx context.Context,
	tx *sql.Tx,
	params model.CreateTrainingBlockParams,
	now time.Time,
) (int64, error) {
	nowUnix := now.UTC().Unix()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO training_blocks (
			player_id, name, description, start_date, end_date, status,
			reminder, notes, retrospective, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'planned', ?, ?, '', ?, ?)
	`, params.PlayerID, params.Name, params.Description, params.StartDate,
		nullableString(params.EndDate), params.Reminder, params.Notes, nowUnix, nowUnix)
	if err != nil {
		return 0, fmt.Errorf("insert training block: %w", err)
	}
	blockID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read training block id: %w", err)
	}
	for i, target := range params.Targets {
		if err := insertTarget(ctx, tx, blockID, i, target); err != nil {
			return 0, err
		}
	}
	return blockID, nil
}

func validateDate(value string) error {
	if _, err := time.Parse(time.DateOnly, value); err != nil {
		return fmt.Errorf("%w: date %q must use YYYY-MM-DD", ErrInvalidInput, value)
	}
	return nil
}

type trainingActivationBounds struct {
	startUnix        int64
	endExclusiveUnix *int64
	localToday       string
}

func activationNow(now time.Time, location *time.Location) (time.Time, error) {
	if location == nil {
		return time.Time{}, fmt.Errorf("%w: activation timezone is required", ErrInvalidInput)
	}
	if now.IsZero() {
		return time.Time{}, fmt.Errorf("%w: activation time is required", ErrInvalidInput)
	}
	return now.In(location), nil
}

func activationDateBounds(
	startDate string,
	endDate sql.NullString,
	now time.Time,
	location *time.Location,
) (trainingActivationBounds, error) {
	localNow, err := activationNow(now, location)
	if err != nil {
		return trainingActivationBounds{}, err
	}
	start, err := time.ParseInLocation(time.DateOnly, startDate, location)
	if err != nil {
		return trainingActivationBounds{}, fmt.Errorf(
			"%w: training block has an invalid start date",
			ErrInvalidInput,
		)
	}
	today := time.Date(
		localNow.Year(),
		localNow.Month(),
		localNow.Day(),
		0,
		0,
		0,
		0,
		location,
	)
	if today.Before(start) {
		return trainingActivationBounds{}, fmt.Errorf(
			"%w: a training block cannot be activated before its start date",
			ErrInvalidInput,
		)
	}

	bounds := trainingActivationBounds{
		startUnix:  start.Unix(),
		localToday: today.Format(time.DateOnly),
	}
	if endDate.Valid {
		end, err := time.ParseInLocation(time.DateOnly, endDate.String, location)
		if err != nil {
			return trainingActivationBounds{}, fmt.Errorf(
				"%w: training block has an invalid end date",
				ErrInvalidInput,
			)
		}
		endExclusive := end.AddDate(0, 0, 1)
		if !today.Before(endExclusive) {
			return trainingActivationBounds{}, fmt.Errorf(
				"%w: an expired training block cannot be activated",
				ErrInvalidInput,
			)
		}
		endExclusiveUnix := endExclusive.Unix()
		bounds.endExclusiveUnix = &endExclusiveUnix
	}
	return bounds, nil
}

func validateTarget(target *model.TrainingTargetInput) error {
	target.Type = strings.ToLower(strings.TrimSpace(target.Type))
	target.Label = strings.TrimSpace(target.Label)
	target.MetricKey = strings.ToLower(strings.TrimSpace(target.MetricKey))
	target.ManualPrompt = strings.TrimSpace(target.ManualPrompt)
	target.Comparator = strings.TrimSpace(target.Comparator)
	target.Unit = strings.TrimSpace(target.Unit)
	target.Aggregation = strings.ToLower(strings.TrimSpace(target.Aggregation))

	if target.Label == "" {
		return fmt.Errorf("%w: label is required", ErrInvalidInput)
	}
	if target.Aggregation == "" {
		target.Aggregation = "per_game"
	}
	switch target.Aggregation {
	case "per_game", "rolling_average", "success_rate":
	default:
		return fmt.Errorf("%w: unsupported aggregation %q", ErrInvalidInput, target.Aggregation)
	}
	if target.WindowGames < 1 {
		target.WindowGames = 1
	}
	switch target.Comparator {
	case "<", "<=", ">", ">=", "=", "yes", "at_least":
	default:
		return fmt.Errorf("%w: unsupported comparator %q", ErrInvalidInput, target.Comparator)
	}

	switch target.Type {
	case model.TargetAutomatic:
		if _, ok := automaticMetricKeys[target.MetricKey]; !ok {
			return fmt.Errorf("%w: unsupported metric %q", ErrInvalidInput, target.MetricKey)
		}
		if target.Threshold == nil {
			return fmt.Errorf("%w: an automatic target needs a threshold", ErrInvalidInput)
		}
	case model.TargetManual:
		if target.ManualPrompt == "" {
			return fmt.Errorf("%w: a manual target needs a prompt", ErrInvalidInput)
		}
		if target.Comparator == "" {
			target.Comparator = "yes"
		}
	default:
		return fmt.Errorf("%w: target type must be automatic or manual", ErrInvalidInput)
	}
	return nil
}

func insertTarget(ctx context.Context, tx *sql.Tx, blockID int64, order int, target model.TrainingTargetInput) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO training_targets (
			block_id, target_type, label, metric_key, manual_prompt, comparator,
			threshold, unit, aggregation, window_games, display_order
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, blockID, target.Type, target.Label, target.MetricKey, target.ManualPrompt,
		target.Comparator, target.Threshold, target.Unit, target.Aggregation,
		target.WindowGames, order)
	if err != nil {
		return fmt.Errorf("insert training target: %w", err)
	}
	return nil
}

// ActivateTrainingBlock atomically enforces the one-active-block rule, locks
// target definitions, and canonically assigns matching unassigned games.
func (s *Store) ActivateTrainingBlock(ctx context.Context, blockID int64, replaceActive bool) (*model.TrainingBlock, error) {
	return s.ActivateTrainingBlockAt(
		ctx,
		blockID,
		replaceActive,
		time.Now(),
		time.UTC,
	)
}

// ActivateTrainingBlockAt activates a block using location for all calendar
// boundaries. now is explicit so callers can use their configured timezone and
// tests do not depend on the process clock.
func (s *Store) ActivateTrainingBlockAt(
	ctx context.Context,
	blockID int64,
	replaceActive bool,
	now time.Time,
	location *time.Location,
) (*model.TrainingBlock, error) {
	if _, err := activationNow(now, location); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin activate training block: %w", err)
	}
	defer tx.Rollback()

	if err := activateTrainingBlockTx(ctx, tx, blockID, replaceActive, now, location); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit training block activation: %w", err)
	}
	return s.trainingBlock(ctx, blockID)
}

func activateTrainingBlockTx(
	ctx context.Context,
	tx *sql.Tx,
	blockID int64,
	replaceActive bool,
	now time.Time,
	location *time.Location,
) error {
	var playerID int64
	var status, startDate string
	var endDate sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT player_id, status, start_date, end_date
		FROM training_blocks
		WHERE id = ?
	`, blockID).Scan(&playerID, &status, &startDate, &endDate)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("get training block for activation: %w", err)
	}
	bounds, err := activationDateBounds(startDate, endDate, now, location)
	if err != nil {
		return err
	}
	if status == model.TrainingBlockActive {
		return nil
	}
	if status != model.TrainingBlockPlanned {
		return fmt.Errorf("%w: only a planned block can be activated", ErrInvalidInput)
	}

	var targetCount int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM training_targets WHERE block_id = ?",
		blockID,
	).Scan(&targetCount); err != nil {
		return fmt.Errorf("count training targets: %w", err)
	}
	if targetCount == 0 {
		return ErrTrainingBlockNeedsTarget
	}

	var activeID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM training_blocks
		WHERE player_id = ? AND status = 'active' AND id <> ?
	`, playerID, blockID).Scan(&activeID)
	switch {
	case err == nil:
		if !replaceActive {
			return fmt.Errorf("%w: block %d is active", ErrActiveTrainingBlock, activeID)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE training_blocks
			SET status = 'completed', end_date = ?, updated_at = ?
			WHERE id = ? AND status = 'active'
		`, bounds.localToday, now.UTC().Unix(), activeID); err != nil {
			return fmt.Errorf("complete replaced training block: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		// No block needs replacing.
	default:
		return fmt.Errorf("check active training block: %w", err)
	}

	nowUnix := now.UTC().Unix()
	if _, err := tx.ExecContext(ctx,
		"UPDATE training_blocks SET status = 'active', updated_at = ? WHERE id = ?",
		nowUnix, blockID,
	); err != nil {
		return fmt.Errorf("activate training block: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO match_training_blocks (
			match_id, player_id, block_id, assignment_source, assigned_at
		)
		SELECT m.id, pms.player_id, ?, 'time', ?
		FROM matches m
		JOIN player_match_stats pms ON pms.match_id = m.id
		LEFT JOIN match_training_blocks mtb
		  ON mtb.match_id = m.id AND mtb.player_id = pms.player_id
		WHERE pms.player_id = ?
		  AND mtb.match_id IS NULL
		  AND m.game_start_at >= ?
		  AND (? IS NULL OR m.game_start_at < ?)
	`, blockID, nowUnix, playerID, bounds.startUnix,
		nullableInt64(bounds.endExclusiveUnix), nullableInt64(bounds.endExclusiveUnix))
	if err != nil {
		return fmt.Errorf("assign matches to training block: %w", err)
	}

	if err := recomputeBlockTargetResults(ctx, tx, blockID); err != nil {
		return err
	}
	return nil
}

// ActiveTrainingBlock returns nil without an error when the player has no
// current focus.
func (s *Store) ActiveTrainingBlock(ctx context.Context, playerID int64) (*model.TrainingBlock, error) {
	if playerID == 0 {
		player, err := s.PrimaryPlayer(ctx)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, nil
			}
			return nil, err
		}
		playerID = player.ID
	}

	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM training_blocks
		WHERE player_id = ? AND status = 'active'
	`, playerID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find active training block: %w", err)
	}
	return s.trainingBlock(ctx, id)
}

// ListTrainingBlocks returns newest blocks first with their target definitions.
func (s *Store) ListTrainingBlocks(ctx context.Context, playerID int64) ([]model.TrainingBlock, error) {
	if playerID == 0 {
		player, err := s.PrimaryPlayer(ctx)
		if err != nil {
			return nil, err
		}
		playerID = player.ID
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, player_id, name, description, start_date, end_date, status,
		       reminder, notes, retrospective, created_at, updated_at
		FROM training_blocks
		WHERE player_id = ?
		ORDER BY
			CASE status WHEN 'active' THEN 0 WHEN 'planned' THEN 1 ELSE 2 END,
			start_date DESC, id DESC
	`, playerID)
	if err != nil {
		return nil, fmt.Errorf("list training blocks: %w", err)
	}
	var blocks []model.TrainingBlock
	for rows.Next() {
		block, err := scanTrainingBlock(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan training block: %w", err)
		}
		blocks = append(blocks, block)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close training blocks: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate training blocks: %w", err)
	}
	for i := range blocks {
		blocks[i].Targets, err = s.trainingTargets(ctx, blocks[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return blocks, nil
}

func (s *Store) trainingBlock(ctx context.Context, blockID int64) (*model.TrainingBlock, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, player_id, name, description, start_date, end_date, status,
		       reminder, notes, retrospective, created_at, updated_at
		FROM training_blocks
		WHERE id = ?
	`, blockID)
	block, err := scanTrainingBlock(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get training block: %w", err)
	}
	block.Targets, err = s.trainingTargets(ctx, block.ID)
	if err != nil {
		return nil, err
	}
	return &block, nil
}

func scanTrainingBlock(row rowScanner) (model.TrainingBlock, error) {
	var block model.TrainingBlock
	var endDate sql.NullString
	var createdAt, updatedAt int64
	err := row.Scan(
		&block.ID, &block.PlayerID, &block.Name, &block.Description,
		&block.StartDate, &endDate, &block.Status, &block.Reminder, &block.Notes,
		&block.Retrospective, &createdAt, &updatedAt,
	)
	if endDate.Valid {
		block.EndDate = &endDate.String
	}
	block.CreatedAt = unixTime(createdAt)
	block.UpdatedAt = unixTime(updatedAt)
	return block, err
}

func (s *Store) trainingTargets(ctx context.Context, blockID int64) ([]model.TrainingTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, block_id, target_type, label, metric_key, manual_prompt,
		       comparator, threshold, unit, aggregation, window_games, display_order
		FROM training_targets
		WHERE block_id = ?
		ORDER BY display_order, id
	`, blockID)
	if err != nil {
		return nil, fmt.Errorf("list training targets: %w", err)
	}
	defer rows.Close()

	var targets []model.TrainingTarget
	for rows.Next() {
		var target model.TrainingTarget
		var threshold sql.NullFloat64
		if err := rows.Scan(
			&target.ID, &target.BlockID, &target.Type, &target.Label,
			&target.MetricKey, &target.ManualPrompt, &target.Comparator,
			&threshold, &target.Unit, &target.Aggregation, &target.WindowGames,
			&target.DisplayOrder,
		); err != nil {
			return nil, fmt.Errorf("scan training target: %w", err)
		}
		if threshold.Valid {
			target.Threshold = &threshold.Float64
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate training targets: %w", err)
	}
	return targets, nil
}

type evaluationInput struct {
	targetID       int64
	matchID        int64
	metricKey      string
	comparator     string
	threshold      float64
	duration       int
	isRemake       bool
	win            bool
	kills          int
	deaths         int
	assists        int
	cs             int
	gold           int
	championDamage int
	visionScore    int
	controlWards   int
}

func recomputeBlockTargetResults(ctx context.Context, tx *sql.Tx, blockID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT tt.id, m.id, tt.metric_key, tt.comparator, tt.threshold,
		       m.duration_seconds, m.is_remake, pms.win, pms.kills, pms.deaths,
		       pms.assists, pms.lane_minions + pms.neutral_minions, pms.gold,
		       pms.champion_damage, pms.vision_score, pms.vision_wards_bought
		FROM training_targets tt
		JOIN match_training_blocks mtb ON mtb.block_id = tt.block_id
		JOIN matches m ON m.id = mtb.match_id
		JOIN player_match_stats pms
		  ON pms.match_id = mtb.match_id AND pms.player_id = mtb.player_id
		WHERE tt.block_id = ?
		  AND tt.target_type = 'automatic'
		  AND tt.aggregation = 'per_game'
		ORDER BY tt.id, m.id
	`, blockID)
	if err != nil {
		return fmt.Errorf("load target evaluation inputs: %w", err)
	}

	var inputs []evaluationInput
	for rows.Next() {
		var input evaluationInput
		var remake, win int
		if err := rows.Scan(
			&input.targetID, &input.matchID, &input.metricKey, &input.comparator,
			&input.threshold, &input.duration, &remake, &win, &input.kills,
			&input.deaths, &input.assists, &input.cs, &input.gold,
			&input.championDamage, &input.visionScore, &input.controlWards,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan target evaluation input: %w", err)
		}
		input.isRemake = remake == 1
		input.win = win == 1
		inputs = append(inputs, input)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close target evaluation inputs: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate target evaluation inputs: %w", err)
	}

	now := time.Now().UTC().Unix()
	for _, input := range inputs {
		state := "unknown"
		var actual *float64
		if input.isRemake {
			state = "not_applicable"
		} else if value, available := metricValue(input); available {
			actual = &value
			if compareMetric(value, input.comparator, input.threshold) {
				state = "met"
			} else {
				state = "missed"
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO target_results (
				target_id, match_id, actual_value, result_state,
				evaluator_version, is_current, evaluated_at
			) VALUES (?, ?, ?, ?, 1, 1, ?)
			ON CONFLICT(target_id, match_id, evaluator_version) DO UPDATE SET
				actual_value = excluded.actual_value,
				result_state = excluded.result_state,
				is_current = 1,
				evaluated_at = excluded.evaluated_at
		`, input.targetID, input.matchID, actual, state, now)
		if err != nil {
			return fmt.Errorf("save target result: %w", err)
		}
	}
	return nil
}

func metricValue(input evaluationInput) (float64, bool) {
	switch input.metricKey {
	case "deaths":
		return float64(input.deaths), true
	case "kda":
		return float64(input.kills+input.assists) / float64(max(1, input.deaths)), true
	case "vision_per_minute":
		if input.duration <= 0 {
			return 0, false
		}
		return float64(input.visionScore) / (float64(input.duration) / 60), true
	case "control_wards":
		return float64(input.controlWards), true
	case "cs":
		return float64(input.cs), true
	case "gold":
		return float64(input.gold), true
	case "champion_damage":
		return float64(input.championDamage), true
	case "win":
		if input.win {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func compareMetric(actual float64, comparator string, threshold float64) bool {
	switch comparator {
	case "<":
		return actual < threshold
	case "<=":
		return actual <= threshold
	case ">":
		return actual > threshold
	case ">=", "at_least":
		return actual >= threshold
	case "=", "yes":
		return math.Abs(actual-threshold) < 1e-9
	default:
		return false
	}
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
