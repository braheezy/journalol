package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"journalol/internal/model"
)

// MistakeCategories lists the active built-in and user-defined review tags.
func (s *Store) MistakeCategories(ctx context.Context) ([]model.MistakeCategory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, label, is_active, is_custom
		FROM mistake_categories
		WHERE is_active = 1
		ORDER BY is_custom, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list mistake categories: %w", err)
	}
	defer rows.Close()

	categories := make([]model.MistakeCategory, 0)
	for rows.Next() {
		var category model.MistakeCategory
		var active, custom int
		if err := rows.Scan(
			&category.ID, &category.Slug, &category.Label, &active, &custom,
		); err != nil {
			return nil, fmt.Errorf("scan mistake category: %w", err)
		}
		category.IsActive = active == 1
		category.IsCustom = custom == 1
		categories = append(categories, category)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mistake categories: %w", err)
	}
	return categories, nil
}

// UpsertReview saves a draft or completed review and atomically replaces its
// annotation set.
func (s *Store) UpsertReview(ctx context.Context, params model.UpsertReviewParams) (*model.Review, error) {
	if params.MatchID == 0 {
		return nil, fmt.Errorf("%w: match is required", ErrInvalidInput)
	}
	params.BiggestMistake = strings.TrimSpace(params.BiggestMistake)
	params.DoneWell = strings.TrimSpace(params.DoneWell)
	params.NextGame = strings.TrimSpace(params.NextGame)
	seenCheckins := make(map[int64]struct{}, len(params.ManualTargetCheckins))
	for index := range params.ManualTargetCheckins {
		checkin := &params.ManualTargetCheckins[index]
		checkin.Note = strings.TrimSpace(checkin.Note)
		if checkin.TargetID <= 0 {
			return nil, fmt.Errorf("%w: invalid manual target id", ErrInvalidInput)
		}
		if _, exists := seenCheckins[checkin.TargetID]; exists {
			return nil, fmt.Errorf("%w: duplicate manual target check-in", ErrInvalidInput)
		}
		seenCheckins[checkin.TargetID] = struct{}{}
	}

	scale, grade, normalized, err := normalizeGrade(params.GradeScale, params.Grade)
	if err != nil {
		return nil, err
	}
	if params.Complete {
		if normalized == nil {
			return nil, fmt.Errorf("%w: a grade is required to finish a review", ErrInvalidInput)
		}
		if params.BiggestMistake == "" && params.DoneWell == "" && params.NextGame == "" {
			return nil, fmt.Errorf("%w: add at least one reflection before finishing", ErrInvalidInput)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin review save: %w", err)
	}
	defer tx.Rollback()

	if params.PlayerID == 0 {
		err := tx.QueryRowContext(ctx, `
			SELECT pms.player_id
			FROM player_match_stats pms
			JOIN player_profiles p ON p.id = pms.player_id
			WHERE pms.match_id = ? AND p.is_primary = 1
		`, params.MatchID).Scan(&params.PlayerID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("resolve review player: %w", err)
		}
	} else {
		var ownsMatch int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM player_match_stats
				WHERE match_id = ? AND player_id = ?
			)
		`, params.MatchID, params.PlayerID).Scan(&ownsMatch); err != nil {
			return nil, fmt.Errorf("validate review match: %w", err)
		}
		if ownsMatch == 0 {
			return nil, ErrNotFound
		}
	}
	if err := validateManualTargetCheckins(
		ctx,
		tx,
		params.MatchID,
		params.PlayerID,
		params.ManualTargetCheckins,
	); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Unix()
	var completedAt any
	if params.Complete {
		completedAt = now
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO match_reviews (
			match_id, player_id, grade_scale, grade_value, grade_normalized,
			biggest_mistake, done_well, next_game, drafted_at, completed_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id, player_id) DO UPDATE SET
			grade_scale = excluded.grade_scale,
			grade_value = excluded.grade_value,
			grade_normalized = excluded.grade_normalized,
			biggest_mistake = excluded.biggest_mistake,
			done_well = excluded.done_well,
			next_game = excluded.next_game,
			completed_at = CASE
				WHEN excluded.completed_at IS NULL THEN NULL
				ELSE COALESCE(match_reviews.completed_at, excluded.completed_at)
			END,
			updated_at = excluded.updated_at
	`, params.MatchID, params.PlayerID, nullableText(scale), nullableText(grade),
		normalized, params.BiggestMistake, params.DoneWell, params.NextGame,
		now, completedAt, now, now)
	if err != nil {
		return nil, fmt.Errorf("upsert match review: %w", err)
	}

	var reviewID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM match_reviews WHERE match_id = ? AND player_id = ?
	`, params.MatchID, params.PlayerID).Scan(&reviewID); err != nil {
		return nil, fmt.Errorf("read saved review id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM review_annotations WHERE review_id = ?",
		reviewID,
	); err != nil {
		return nil, fmt.Errorf("replace review annotations: %w", err)
	}

	annotations := make([]model.ReviewAnnotationInput, 0, len(params.CategoryIDs)+len(params.Annotations))
	seenWholeMatch := make(map[int64]struct{}, len(params.CategoryIDs))
	for _, categoryID := range params.CategoryIDs {
		if categoryID <= 0 {
			return nil, fmt.Errorf("%w: invalid category id", ErrInvalidInput)
		}
		if _, seen := seenWholeMatch[categoryID]; seen {
			continue
		}
		seenWholeMatch[categoryID] = struct{}{}
		annotations = append(annotations, model.ReviewAnnotationInput{CategoryID: categoryID})
	}
	for _, annotation := range params.Annotations {
		if annotation.CategoryID <= 0 {
			return nil, fmt.Errorf("%w: invalid annotation category", ErrInvalidInput)
		}
		if annotation.EventTimestampSeconds == nil && annotation.DeathSequence == nil {
			if _, seen := seenWholeMatch[annotation.CategoryID]; seen {
				continue
			}
			seenWholeMatch[annotation.CategoryID] = struct{}{}
		}
		annotation.Note = strings.TrimSpace(annotation.Note)
		annotations = append(annotations, annotation)
	}
	for _, annotation := range annotations {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO review_annotations (
				review_id, category_id, event_timestamp_seconds,
				death_sequence, note, created_at
			) VALUES (?, ?, ?, ?, ?, ?)
		`, reviewID, annotation.CategoryID, annotation.EventTimestampSeconds,
			annotation.DeathSequence, annotation.Note, now)
		if err != nil {
			return nil, fmt.Errorf("insert review annotation: %w", err)
		}
	}
	for _, checkin := range params.ManualTargetCheckins {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO target_checkins (
				target_id, match_id, review_id, boolean_value, rating_value,
				note, source, created_at, updated_at
			) VALUES (?, ?, ?, ?, NULL, ?, 'manual', ?, ?)
			ON CONFLICT(target_id, match_id) DO UPDATE SET
				review_id = excluded.review_id,
				boolean_value = excluded.boolean_value,
				rating_value = NULL,
				note = excluded.note,
				source = 'manual',
				updated_at = excluded.updated_at
		`, checkin.TargetID, params.MatchID, reviewID, boolInt(checkin.Value),
			checkin.Note, now, now)
		if err != nil {
			return nil, fmt.Errorf("upsert manual target check-in: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit review: %w", err)
	}
	return s.reviewForMatch(ctx, params.MatchID, params.PlayerID)
}

func validateManualTargetCheckins(
	ctx context.Context,
	tx *sql.Tx,
	matchID int64,
	playerID int64,
	checkins []model.ManualTargetCheckinInput,
) error {
	for _, checkin := range checkins {
		var valid int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM match_training_blocks mtb
				JOIN training_blocks tb
				  ON tb.id = mtb.block_id AND tb.player_id = mtb.player_id
				JOIN training_targets tt
				  ON tt.block_id = mtb.block_id
				WHERE mtb.match_id = ?
				  AND mtb.player_id = ?
				  AND tt.id = ?
				  AND tt.target_type = 'manual'
			)
		`, matchID, playerID, checkin.TargetID).Scan(&valid); err != nil {
			return fmt.Errorf("validate manual target check-in: %w", err)
		}
		if valid != 1 {
			return fmt.Errorf(
				"%w: manual target %d does not belong to this match's training block",
				ErrInvalidInput,
				checkin.TargetID,
			)
		}
	}
	return nil
}

func normalizeGrade(scale, grade string) (string, string, *float64, error) {
	scale = strings.ToLower(strings.TrimSpace(scale))
	grade = strings.ToUpper(strings.TrimSpace(grade))
	if grade == "" {
		if scale != "" && scale != model.GradeNumeric && scale != model.GradeLetter {
			return "", "", nil, fmt.Errorf("%w: unsupported grade scale", ErrInvalidInput)
		}
		return scale, "", nil, nil
	}
	if scale == "" {
		if _, err := strconv.Atoi(grade); err == nil {
			scale = model.GradeNumeric
		} else {
			scale = model.GradeLetter
		}
	}

	var value float64
	switch scale {
	case model.GradeNumeric:
		number, err := strconv.Atoi(grade)
		if err != nil || number < 1 || number > 5 {
			return "", "", nil, fmt.Errorf("%w: numeric grade must be 1–5", ErrInvalidInput)
		}
		value = float64(number)
	case model.GradeLetter:
		letterValues := map[string]float64{"A": 5, "B": 4, "C": 3, "D": 2, "F": 1}
		var ok bool
		value, ok = letterValues[grade]
		if !ok {
			return "", "", nil, fmt.Errorf("%w: letter grade must be A, B, C, D, or F", ErrInvalidInput)
		}
	default:
		return "", "", nil, fmt.Errorf("%w: unsupported grade scale", ErrInvalidInput)
	}
	return scale, grade, &value, nil
}

func (s *Store) reviewForMatch(ctx context.Context, matchID, playerID int64) (*model.Review, error) {
	var review model.Review
	var scale, grade sql.NullString
	var normalized sql.NullFloat64
	var completedAt sql.NullInt64
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, match_id, player_id, grade_scale, grade_value,
		       grade_normalized, biggest_mistake, done_well, next_game,
		       completed_at, created_at, updated_at
		FROM match_reviews
		WHERE match_id = ? AND player_id = ?
	`, matchID, playerID).Scan(
		&review.ID, &review.MatchID, &review.PlayerID, &scale, &grade,
		&normalized, &review.BiggestMistake, &review.DoneWell,
		&review.NextGame, &completedAt, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get match review: %w", err)
	}
	if scale.Valid {
		review.GradeScale = scale.String
	}
	if grade.Valid {
		review.Grade = grade.String
	}
	if normalized.Valid {
		review.GradeNormalized = &normalized.Float64
	}
	review.CreatedAt = unixTime(createdAt)
	review.UpdatedAt = unixTime(updatedAt)
	if completedAt.Valid {
		value := unixTime(completedAt.Int64)
		review.CompletedAt = &value
		review.Complete = true
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT ra.id, ra.review_id, ra.category_id, mc.slug, mc.label,
		       ra.event_timestamp_seconds, ra.death_sequence, ra.note
		FROM review_annotations ra
		JOIN mistake_categories mc ON mc.id = ra.category_id
		WHERE ra.review_id = ?
		ORDER BY ra.id
	`, review.ID)
	if err != nil {
		return nil, fmt.Errorf("list review annotations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var annotation model.ReviewAnnotation
		var timestamp, deathSequence sql.NullInt64
		if err := rows.Scan(
			&annotation.ID, &annotation.ReviewID, &annotation.CategoryID,
			&annotation.CategorySlug, &annotation.CategoryLabel, &timestamp,
			&deathSequence, &annotation.Note,
		); err != nil {
			return nil, fmt.Errorf("scan review annotation: %w", err)
		}
		if timestamp.Valid {
			value := int(timestamp.Int64)
			annotation.EventTimestampSeconds = &value
		}
		if deathSequence.Valid {
			value := int(deathSequence.Int64)
			annotation.DeathSequence = &value
		}
		review.Annotations = append(review.Annotations, annotation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate review annotations: %w", err)
	}
	return &review, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
