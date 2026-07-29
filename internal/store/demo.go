package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"journalol/internal/model"
)

type demoMatch struct {
	champion     string
	championID   int
	opponent     string
	win          bool
	kills        int
	deaths       int
	assists      int
	duration     int
	vision       int
	controlWards int
	wardsPlaced  int
	wardsKilled  int
	cs           int
	gold         int
	damage       int
	queue        string
	isRemake     bool
	completeness string
}

var demoMatches = []demoMatch{
	{"Leona", 89, "Nautilus", false, 1, 9, 8, 780, 12, 1, 7, 1, 18, 4100, 3900, "Ranked Solo", true, "complete"},
	{"Nami", 267, "Thresh", false, 2, 8, 13, 1920, 48, 2, 22, 5, 31, 8200, 7800, "Ranked Solo", false, "complete"},
	{"Rakan", 497, "Lulu", true, 3, 7, 17, 2040, 55, 2, 26, 6, 36, 9100, 8400, "Ranked Solo", false, "complete"},
	{"Thresh", 412, "Milio", false, 1, 7, 11, 1860, 46, 2, 21, 4, 29, 7900, 6900, "Normal Draft", false, "complete"},
	{"Nami", 267, "Nautilus", true, 2, 6, 19, 2160, 62, 3, 29, 7, 42, 9800, 9700, "Ranked Solo", false, "complete"},
	{"Leona", 89, "Janna", false, 0, 8, 9, 1740, 41, 2, 18, 3, 25, 7300, 5100, "Ranked Solo", false, "complete"},
	{"Rakan", 497, "Pyke", true, 4, 7, 20, 2220, 67, 3, 31, 8, 39, 10100, 10500, "Ranked Solo", false, "partial_timeline"},
	{"Thresh", 412, "Lulu", false, 2, 6, 15, 1980, 58, 3, 27, 6, 33, 8700, 8200, "Ranked Solo", false, "complete"},
	{"Nami", 267, "Rell", true, 1, 8, 22, 2280, 71, 3, 34, 8, 45, 10600, 11300, "Ranked Solo", false, "complete"},
	{"Leona", 89, "Milio", false, 2, 7, 12, 1900, 51, 2, 24, 5, 28, 8100, 7600, "Normal Draft", false, "complete"},
	{"Nami", 267, "Thresh", true, 3, 6, 21, 2100, 69, 3, 32, 7, 41, 9900, 10400, "Ranked Solo", false, "complete"},
	{"Rakan", 497, "Nautilus", true, 2, 5, 18, 2010, 66, 3, 30, 7, 38, 9300, 8800, "Ranked Solo", false, "complete"},
	{"Thresh", 412, "Lulu", false, 1, 4, 13, 1810, 59, 3, 28, 6, 31, 8000, 7200, "Ranked Solo", false, "complete"},
	{"Nami", 267, "Rell", true, 2, 5, 24, 2240, 78, 4, 37, 9, 46, 10700, 11900, "Ranked Solo", false, "complete"},
	{"Leona", 89, "Janna", true, 3, 4, 16, 1960, 65, 3, 31, 7, 35, 9100, 9000, "Normal Draft", false, "complete"},
	{"Rakan", 497, "Pyke", true, 4, 3, 22, 2180, 76, 4, 36, 9, 43, 10400, 11100, "Ranked Solo", false, "complete"},
	{"Nami", 267, "Milio", false, 1, 5, 14, 1880, 64, 3, 30, 6, 32, 8300, 7400, "Ranked Solo", false, "complete"},
	{"Thresh", 412, "Lulu", true, 2, 4, 19, 2070, 73, 4, 35, 8, 37, 9700, 9600, "Ranked Solo", false, "complete"},
	{"Nami", 267, "Nautilus", true, 3, 3, 26, 2310, 86, 4, 40, 10, 49, 11200, 12700, "Ranked Solo", false, "complete"},
	{"Rakan", 497, "Rell", true, 2, 4, 21, 2130, 79, 4, 38, 9, 40, 10200, 10800, "Ranked Solo", false, "complete"},
}

// SeedDemo installs one wholly synthetic profile and 20 games. It is
// idempotent for its own profile and refuses to coexist with real player data.
func (s *Store) SeedDemo(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin demo seed: %w", err)
	}
	defer tx.Rollback()

	var profileCount, nonDemoCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN is_demo = 0 THEN 1 ELSE 0 END), 0)
		FROM player_profiles
	`).Scan(&profileCount, &nonDemoCount); err != nil {
		return fmt.Errorf("inspect profiles before demo seed: %w", err)
	}
	if nonDemoCount > 0 {
		return ErrDemoProfileConflict
	}
	if profileCount > 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM player_profiles
			WHERE is_demo = 1 AND puuid = ?
		`, demoPUUID).Scan(&count); err != nil {
			return fmt.Errorf("inspect existing demo profile: %w", err)
		}
		if profileCount == 1 && count == 1 {
			return tx.Commit()
		}
		return ErrDemoProfileConflict
	}

	seededAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO player_profiles (
			game_name, tag_line, platform_route, regional_route, puuid,
			profile_icon_id, summoner_level, is_primary, is_demo,
			poll_interval_mins, history_limit, created_at, updated_at
		) VALUES ('PracticePal', 'FOCUS', 'NA1', 'AMERICAS', ?, 29, 247, 1, 1, 5, 20, ?, ?)
	`, demoPUUID, seededAt.Unix(), seededAt.Unix())
	if err != nil {
		return fmt.Errorf("insert demo player: %w", err)
	}
	playerID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("read demo player id: %w", err)
	}

	blockID, automaticTargetID, manualTargetID, err := seedDemoTrainingBlock(ctx, tx, playerID, seededAt)
	if err != nil {
		return err
	}

	categoryIDs, err := demoCategoryIDs(ctx, tx)
	if err != nil {
		return err
	}

	matchIDs := make([]int64, 0, len(demoMatches))
	for index, fixture := range demoMatches {
		matchID, err := seedDemoMatch(ctx, tx, playerID, blockID, seededAt, index, fixture)
		if err != nil {
			return err
		}
		matchIDs = append(matchIDs, matchID)
	}
	if err := seedDemoReviews(ctx, tx, playerID, matchIDs, manualTargetID, categoryIDs, seededAt); err != nil {
		return err
	}
	if err := recomputeBlockTargetResults(ctx, tx, blockID); err != nil {
		return err
	}

	var resultCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM target_results WHERE target_id = ?
	`, automaticTargetID).Scan(&resultCount); err != nil {
		return fmt.Errorf("verify demo target results: %w", err)
	}
	if resultCount != len(demoMatches) {
		return fmt.Errorf("verify demo target results: got %d, want %d", resultCount, len(demoMatches))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit demo seed: %w", err)
	}
	return nil
}

func seedDemoTrainingBlock(ctx context.Context, tx *sql.Tx, playerID int64, seededAt time.Time) (int64, int64, int64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO training_blocks (
			player_id, name, description, start_date, end_date, status,
			reminder, notes, retrospective, created_at, updated_at
		) VALUES (
			?, 'Safer support setup',
			'Spend twenty games making deaths intentional and arriving with useful vision.',
			'2026-07-01', NULL, 'planned',
			'Before walking into fog: who can be here, and what do we gain?',
			'Synthetic demo focus for exploring the training loop.', '', ?, ?
		)
	`, playerID, seededAt.Unix(), seededAt.Unix())
	if err != nil {
		return 0, 0, 0, fmt.Errorf("insert demo training block: %w", err)
	}
	blockID, err := result.LastInsertId()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("read demo block id: %w", err)
	}

	five := 5.0
	automatic := modelTargetInput(
		"automatic", "Fewer than 5 deaths", "deaths", "", "<", &five, "deaths",
	)
	if err := insertTarget(ctx, tx, blockID, 0, automatic); err != nil {
		return 0, 0, 0, err
	}
	manual := modelTargetInput(
		"manual", "Avoid unnecessary facechecks", "",
		"Did I avoid walking into fog without a reason?", "yes", nil, "",
	)
	if err := insertTarget(ctx, tx, blockID, 1, manual); err != nil {
		return 0, 0, 0, err
	}

	var automaticID, manualID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM training_targets
		WHERE block_id = ? AND metric_key = 'deaths'
	`, blockID).Scan(&automaticID); err != nil {
		return 0, 0, 0, fmt.Errorf("read demo automatic target: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM training_targets
		WHERE block_id = ? AND target_type = 'manual'
	`, blockID).Scan(&manualID); err != nil {
		return 0, 0, 0, fmt.Errorf("read demo manual target: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE training_blocks SET status = 'active' WHERE id = ?
	`, blockID); err != nil {
		return 0, 0, 0, fmt.Errorf("activate demo block: %w", err)
	}
	return blockID, automaticID, manualID, nil
}

func modelTargetInput(targetType, label, metric, prompt, comparator string, threshold *float64, unit string) model.TrainingTargetInput {
	return model.TrainingTargetInput{
		Type:         targetType,
		Label:        label,
		MetricKey:    metric,
		ManualPrompt: prompt,
		Comparator:   comparator,
		Threshold:    threshold,
		Unit:         unit,
		Aggregation:  "per_game",
		WindowGames:  1,
	}
}

func demoCategoryIDs(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, slug FROM mistake_categories WHERE is_active = 1
	`)
	if err != nil {
		return nil, fmt.Errorf("load demo categories: %w", err)
	}
	categories := make(map[string]int64)
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan demo category: %w", err)
		}
		categories[slug] = id
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close demo categories: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate demo categories: %w", err)
	}
	return categories, nil
}

func seedDemoMatch(
	ctx context.Context,
	tx *sql.Tx,
	playerID int64,
	blockID int64,
	seededAt time.Time,
	index int,
	fixture demoMatch,
) (int64, error) {
	startedAt := time.Date(2026, time.July, index+1, 19+(index%3), 10, 0, 0, time.UTC)
	endedAt := startedAt.Add(time.Duration(fixture.duration) * time.Second)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO matches (
			riot_match_id, queue_id, queue_type, map_id, game_mode, game_type,
			patch, game_start_at, game_end_at, duration_seconds, is_remake,
			surrendered, completeness, normalizer_version, imported_at, updated_at
		) VALUES (?, ?, ?, 11, 'CLASSIC', 'MATCHED_GAME', '26.14', ?, ?, ?, ?, 0, ?, 1, ?, ?)
	`, fmt.Sprintf("DEMO_NA1_%04d", index+1), demoQueueID(fixture.queue),
		fixture.queue, startedAt.Unix(), endedAt.Unix(), fixture.duration,
		boolInt(fixture.isRemake), fixture.completeness, seededAt.Unix(), seededAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("insert demo match %d: %w", index+1, err)
	}
	matchID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read demo match %d id: %w", index+1, err)
	}

	laneMinions := fixture.cs
	items := "[3850,2003,2055,1001,1028]"
	runes := "[8465,8463,8444,8453,8347,8316]"
	spells := "[4,14]"
	skills := "[3,1,2,3,3,4,3,1,3,1,4,1,1,2,2,4,2,2]"
	if fixture.completeness == "partial_timeline" {
		skills = "[]"
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO player_match_stats (
			match_id, player_id, participant_id, team_id, champion_id,
			champion_name, role, win, kills, deaths, assists, lane_minions,
			neutral_minions, gold, champion_damage, vision_score, wards_placed,
			wards_killed, vision_wards_bought, opponent_champion,
			final_items_json, runes_json, summoner_spells_json, skill_order_json
		) VALUES (?, ?, 5, 100, ?, ?, 'UTILITY', ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, matchID, playerID, fixture.championID, fixture.champion, boolInt(fixture.win),
		fixture.kills, fixture.deaths, fixture.assists, laneMinions, fixture.gold,
		fixture.damage, fixture.vision, fixture.wardsPlaced, fixture.wardsKilled,
		fixture.controlWards, fixture.opponent, items, runes, spells, skills)
	if err != nil {
		return 0, fmt.Errorf("insert demo stats %d: %w", index+1, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO match_training_blocks (
			match_id, player_id, block_id, assignment_source, assigned_at
		) VALUES (?, ?, ?, 'demo', ?)
	`, matchID, playerID, blockID, seededAt.Unix()); err != nil {
		return 0, fmt.Errorf("link demo match %d to block: %w", index+1, err)
	}
	return matchID, nil
}

func seedDemoReviews(
	ctx context.Context,
	tx *sql.Tx,
	playerID int64,
	matchIDs []int64,
	manualTargetID int64,
	categories map[string]int64,
	seededAt time.Time,
) error {
	categoryRotation := []string{
		"facecheck", "positioning", "greed", "no-vision", "late-to-objective",
		"bad-engage", "jungle-tracking", "mechanical-error",
	}
	// Review fifteen eligible historical games. The newest four remain in the
	// queue, and the remake is deliberately not presented as training evidence.
	for index := 1; index <= 15; index++ {
		matchID := matchIDs[index]
		grade := "C"
		normalized := 3.0
		if index >= 11 {
			grade = "B"
			normalized = 4
		}
		if index >= 14 {
			grade = "A"
			normalized = 5
		}
		completedAt := seededAt.Add(time.Duration(index) * time.Minute).Unix()
		result, err := tx.ExecContext(ctx, `
			INSERT INTO match_reviews (
				match_id, player_id, grade_scale, grade_value, grade_normalized,
				biggest_mistake, done_well, next_game, drafted_at, completed_at,
				created_at, updated_at
			) VALUES (?, ?, 'letter', ?, ?,
				?, ?, ?, ?, ?, ?, ?)
		`, matchID, playerID, grade, normalized,
			demoMistakeText(index), demoDoneWellText(index), demoNextGameText(index),
			completedAt, completedAt, completedAt, completedAt)
		if err != nil {
			return fmt.Errorf("insert demo review %d: %w", index+1, err)
		}
		reviewID, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read demo review %d id: %w", index+1, err)
		}

		slug := categoryRotation[(index-1)%len(categoryRotation)]
		categoryID, ok := categories[slug]
		if !ok {
			return fmt.Errorf("demo category %q is missing", slug)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_annotations (
				review_id, category_id, event_timestamp_seconds,
				death_sequence, note, created_at
			) VALUES (?, ?, NULL, NULL, '', ?)
		`, reviewID, categoryID, completedAt); err != nil {
			return fmt.Errorf("annotate demo review %d: %w", index+1, err)
		}

		avoidedFacecheck := index >= 9 || index%3 == 0
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO target_checkins (
				target_id, match_id, review_id, boolean_value, rating_value,
				note, source, created_at, updated_at
			) VALUES (?, ?, ?, ?, NULL, '', 'manual', ?, ?)
		`, manualTargetID, matchID, reviewID, boolInt(avoidedFacecheck),
			completedAt, completedAt); err != nil {
			return fmt.Errorf("insert demo target check-in %d: %w", index+1, err)
		}
	}
	return nil
}

func demoQueueID(queue string) int {
	if queue == "Normal Draft" {
		return 400
	}
	return 420
}

func demoMistakeText(index int) string {
	texts := []string{
		"Walked through river without checking who was missing.",
		"Stayed for one extra ward after the play had ended.",
		"Started the engage before our carry was in range.",
		"Used both peel tools on the first target.",
	}
	return texts[index%len(texts)]
}

func demoDoneWellText(index int) string {
	texts := []string{
		"Reset early enough to establish objective vision.",
		"Kept the next wave safe instead of chasing.",
		"Tracked the opposing jungler before roaming.",
		"Saved a defensive cooldown for the carry.",
	}
	return texts[index%len(texts)]
}

func demoNextGameText(index int) string {
	texts := []string{
		"Name the threat before entering fog.",
		"Leave when the objective setup job is complete.",
		"Check teammate distance before committing.",
		"Hold one cooldown for disengage.",
	}
	return texts[index%len(texts)]
}
