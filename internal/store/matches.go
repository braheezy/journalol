package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"journalol/internal/model"
)

const matchSelectColumns = `
	m.id, pms.player_id, m.riot_match_id, m.queue_id, m.queue_type,
	m.game_mode, m.patch, m.game_start_at, m.game_end_at, m.duration_seconds,
	m.is_remake, m.surrendered, m.completeness,
	pms.champion_id, pms.champion_name, pms.role, pms.win, pms.kills,
	pms.deaths, pms.assists, pms.lane_minions + pms.neutral_minions,
	pms.gold, pms.champion_damage, pms.vision_score, pms.wards_placed,
	pms.wards_killed, pms.vision_wards_bought, pms.opponent_champion,
	mtb.block_id, COALESCE(tb.name, ''),
	EXISTS (
		SELECT 1 FROM match_reviews mr
		WHERE mr.match_id = m.id
		  AND mr.player_id = pms.player_id
		  AND mr.completed_at IS NOT NULL
	)
`

const matchSelectJoins = `
	FROM matches m
	JOIN player_match_stats pms ON pms.match_id = m.id
	LEFT JOIN match_training_blocks mtb
	  ON mtb.match_id = m.id AND mtb.player_id = pms.player_id
	LEFT JOIN training_blocks tb ON tb.id = mtb.block_id
`

// RecentMatches returns the primary player's newest imported games.
func (s *Store) RecentMatches(ctx context.Context, playerID int64, limit int) ([]model.Match, error) {
	return s.ListMatches(ctx, model.MatchFilter{PlayerID: playerID, Limit: limit})
}

// ListMatches applies parameterized filters and returns matches newest first.
func (s *Store) ListMatches(ctx context.Context, filter model.MatchFilter) ([]model.Match, error) {
	if filter.PlayerID == 0 {
		player, err := s.PrimaryPlayer(ctx)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return []model.Match{}, nil
			}
			return nil, err
		}
		filter.PlayerID = player.ID
	}
	if filter.Limit <= 0 {
		filter.Limit = defaultMatchLimit
	}
	if filter.Limit > maxMatchLimit {
		filter.Limit = maxMatchLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	var query strings.Builder
	query.WriteString("SELECT ")
	query.WriteString(matchSelectColumns)
	query.WriteString(matchSelectJoins)
	query.WriteString(" WHERE pms.player_id = ?")
	args := []any{filter.PlayerID}

	if champion := strings.TrimSpace(filter.Champion); champion != "" {
		query.WriteString(" AND pms.champion_name = ? COLLATE NOCASE")
		args = append(args, champion)
	}
	if role := strings.TrimSpace(filter.Role); role != "" {
		query.WriteString(" AND pms.role = ? COLLATE NOCASE")
		args = append(args, role)
	}
	if queue := strings.TrimSpace(filter.QueueType); queue != "" {
		query.WriteString(" AND m.queue_type = ? COLLATE NOCASE")
		args = append(args, queue)
	}
	if filter.Result != nil {
		query.WriteString(" AND m.is_remake = 0 AND pms.win = ?")
		args = append(args, boolInt(*filter.Result))
	}
	if filter.From != nil {
		query.WriteString(" AND m.game_start_at >= ?")
		args = append(args, filter.From.UTC().Unix())
	}
	if filter.To != nil {
		query.WriteString(" AND m.game_start_at <= ?")
		args = append(args, filter.To.UTC().Unix())
	}
	if filter.TrainingBlockID != nil {
		query.WriteString(" AND mtb.block_id = ?")
		args = append(args, *filter.TrainingBlockID)
	}
	if filter.Reviewed != nil {
		if *filter.Reviewed {
			query.WriteString(`
				AND EXISTS (
					SELECT 1 FROM match_reviews reviewed
					WHERE reviewed.match_id = m.id
					  AND reviewed.player_id = pms.player_id
					  AND reviewed.completed_at IS NOT NULL
				)
			`)
		} else {
			query.WriteString(`
				AND NOT EXISTS (
					SELECT 1 FROM match_reviews reviewed
					WHERE reviewed.match_id = m.id
					  AND reviewed.player_id = pms.player_id
					  AND reviewed.completed_at IS NOT NULL
				)
			`)
		}
	}
	if notes := strings.TrimSpace(filter.NotesQuery); notes != "" {
		query.WriteString(`
			AND EXISTS (
				SELECT 1
				FROM match_reviews searched_review
				LEFT JOIN review_annotations searched_annotation
				  ON searched_annotation.review_id = searched_review.id
				WHERE searched_review.match_id = m.id
				  AND searched_review.player_id = pms.player_id
				  AND (
				      searched_review.biggest_mistake LIKE ? ESCAPE '\'
				      OR searched_review.done_well LIKE ? ESCAPE '\'
				      OR searched_review.next_game LIKE ? ESCAPE '\'
				      OR searched_annotation.note LIKE ? ESCAPE '\'
				  )
			)
		`)
		pattern := "%" + escapeLike(notes) + "%"
		args = append(args, pattern, pattern, pattern, pattern)
	}
	query.WriteString(" ORDER BY m.game_start_at DESC, m.id DESC LIMIT ? OFFSET ?")
	args = append(args, filter.Limit, filter.Offset)

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list matches: %w", err)
	}
	defer rows.Close()

	matches := make([]model.Match, 0)
	for rows.Next() {
		match, err := scanMatch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan match: %w", err)
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate matches: %w", err)
	}
	return matches, nil
}

// GetMatch returns a match, its display-oriented arrays, and any saved review.
func (s *Store) GetMatch(ctx context.Context, matchID int64) (*model.MatchDetail, error) {
	query := "SELECT " + matchSelectColumns + matchSelectJoins + `
		WHERE m.id = ?
		  AND pms.player_id = (
		      SELECT id FROM player_profiles WHERE is_primary = 1
		  )
	`
	summary, err := scanMatch(s.db.QueryRowContext(ctx, query, matchID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get match: %w", err)
	}

	var itemJSON, runeJSON, spellJSON, skillJSON string
	err = s.db.QueryRowContext(ctx, `
		SELECT final_items_json, runes_json, summoner_spells_json, skill_order_json
		FROM player_match_stats
		WHERE match_id = ? AND player_id = ?
	`, matchID, summary.PlayerID).Scan(&itemJSON, &runeJSON, &spellJSON, &skillJSON)
	if err != nil {
		return nil, fmt.Errorf("get match structures: %w", err)
	}
	detail := &model.MatchDetail{Match: summary}
	if err := decodeIntArray(itemJSON, &detail.Items); err != nil {
		return nil, fmt.Errorf("decode final items: %w", err)
	}
	if err := decodeIntArray(runeJSON, &detail.Runes); err != nil {
		return nil, fmt.Errorf("decode runes: %w", err)
	}
	if err := decodeIntArray(spellJSON, &detail.SummonerSpells); err != nil {
		return nil, fmt.Errorf("decode summoner spells: %w", err)
	}
	if err := decodeIntArray(skillJSON, &detail.SkillOrder); err != nil {
		return nil, fmt.Errorf("decode skill order: %w", err)
	}
	if summary.TrainingBlockID != nil {
		detail.AssignedBlock, err = s.trainingBlock(ctx, *summary.TrainingBlockID)
		if err != nil {
			return nil, fmt.Errorf("get match training block: %w", err)
		}
		detail.ManualTargetCheckins, err = s.manualTargetCheckins(
			ctx,
			matchID,
			summary.PlayerID,
			*summary.TrainingBlockID,
		)
		if err != nil {
			return nil, err
		}
	}

	review, err := s.reviewForMatch(ctx, matchID, summary.PlayerID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if review != nil {
		detail.Review = review
		detail.SelectedCategoryIDs = make([]int64, 0, len(review.Annotations))
		seen := make(map[int64]struct{}, len(review.Annotations))
		for _, annotation := range review.Annotations {
			if _, ok := seen[annotation.CategoryID]; ok {
				continue
			}
			seen[annotation.CategoryID] = struct{}{}
			detail.SelectedCategoryIDs = append(detail.SelectedCategoryIDs, annotation.CategoryID)
		}
	}
	return detail, nil
}

func (s *Store) manualTargetCheckins(
	ctx context.Context,
	matchID int64,
	playerID int64,
	blockID int64,
) ([]model.ManualTargetCheckin, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tt.id, ?, tt.block_id, tt.label, tt.manual_prompt,
		       tc.boolean_value, COALESCE(tc.note, '')
		FROM training_targets tt
		JOIN training_blocks tb ON tb.id = tt.block_id
		LEFT JOIN target_checkins tc
		  ON tc.target_id = tt.id AND tc.match_id = ?
		WHERE tt.block_id = ?
		  AND tb.player_id = ?
		  AND tt.target_type = 'manual'
		ORDER BY tt.display_order, tt.id
	`, matchID, matchID, blockID, playerID)
	if err != nil {
		return nil, fmt.Errorf("list manual target check-ins: %w", err)
	}
	defer rows.Close()

	checkins := make([]model.ManualTargetCheckin, 0)
	for rows.Next() {
		var checkin model.ManualTargetCheckin
		var value sql.NullInt64
		if err := rows.Scan(
			&checkin.TargetID, &checkin.MatchID, &checkin.BlockID,
			&checkin.Label, &checkin.Prompt, &value, &checkin.Note,
		); err != nil {
			return nil, fmt.Errorf("scan manual target check-in: %w", err)
		}
		if value.Valid {
			answered := value.Int64 == 1
			checkin.Value = &answered
		}
		checkins = append(checkins, checkin)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate manual target check-ins: %w", err)
	}
	return checkins, nil
}

func scanMatch(row rowScanner) (model.Match, error) {
	var match model.Match
	var startAt, endAt int64
	var remake, surrendered, win, reviewed int
	var blockID sql.NullInt64
	err := row.Scan(
		&match.ID, &match.PlayerID, &match.RiotMatchID, &match.QueueID,
		&match.QueueType, &match.GameMode, &match.Patch, &startAt, &endAt,
		&match.DurationSeconds, &remake, &surrendered, &match.Completeness,
		&match.ChampionID, &match.Champion, &match.Role, &win, &match.Kills,
		&match.Deaths, &match.Assists, &match.CS, &match.Gold,
		&match.ChampionDamage, &match.VisionScore, &match.WardsPlaced,
		&match.WardsKilled, &match.ControlWards, &match.OpponentChampion,
		&blockID, &match.BlockName, &reviewed,
	)
	match.StartedAt = unixTime(startAt)
	match.EndedAt = unixTime(endAt)
	match.IsRemake = remake == 1
	match.Surrendered = surrendered == 1
	match.Win = win == 1
	match.ReviewComplete = reviewed == 1
	if blockID.Valid {
		match.TrainingBlockID = &blockID.Int64
	}
	return match, err
}

func decodeIntArray(raw string, destination *[]int) error {
	if raw == "" {
		*destination = []int{}
		return nil
	}
	if err := json.Unmarshal([]byte(raw), destination); err != nil {
		return err
	}
	if *destination == nil {
		*destination = []int{}
	}
	return nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
