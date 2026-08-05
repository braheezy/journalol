package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"journalol/internal/model"
)

// DashboardStats calculates controllable summaries from non-remake games.
// WinRate is expressed as a percentage in the range 0–100.
func (s *Store) DashboardStats(ctx context.Context, playerID int64) (*model.DashboardStats, error) {
	if playerID == 0 {
		player, err := s.PrimaryPlayer(ctx)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return &model.DashboardStats{}, nil
			}
			return nil, err
		}
		playerID = player.ID
	}

	stats := &model.DashboardStats{}
	var wins sql.NullInt64
	var averageDeaths, kda, visionPerMinute, controlWards sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			SUM(pms.win),
			AVG(pms.deaths),
			CAST(SUM(pms.kills) + SUM(pms.assists) AS REAL)
				/ MAX(1, SUM(pms.deaths)),
			CASE WHEN SUM(m.duration_seconds) > 0
			     THEN SUM(pms.vision_score) / (SUM(m.duration_seconds) / 60.0)
			     ELSE NULL
			END,
			AVG(pms.vision_wards_bought)
		FROM matches m
		JOIN player_match_stats pms ON pms.match_id = m.id
		WHERE pms.player_id = ? AND m.is_remake = 0
		  AND m.queue_id IN (400, 420, 440)
	`, playerID).Scan(
		&stats.Games, &wins, &averageDeaths, &kda,
		&visionPerMinute, &controlWards,
	)
	if err != nil {
		return nil, fmt.Errorf("calculate dashboard stats: %w", err)
	}
	if wins.Valid {
		stats.Wins = int(wins.Int64)
	}
	if stats.Games > 0 {
		stats.WinRate = float64(stats.Wins) / float64(stats.Games) * 100
	}
	if averageDeaths.Valid {
		stats.AverageDeaths = averageDeaths.Float64
	}
	if kda.Valid {
		stats.KDA = kda.Float64
	}
	if visionPerMinute.Valid {
		stats.VisionPerMinute = visionPerMinute.Float64
	}
	if controlWards.Valid {
		stats.ControlWardsPerGame = controlWards.Float64
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM matches m
		JOIN player_match_stats pms ON pms.match_id = m.id
		LEFT JOIN match_reviews mr
		  ON mr.match_id = m.id AND mr.player_id = pms.player_id
		WHERE pms.player_id = ?
		  AND m.is_remake = 0
		  AND m.queue_id IN (400, 420, 440)
		  AND mr.completed_at IS NULL
	`, playerID).Scan(&stats.PendingReviews); err != nil {
		return nil, fmt.Errorf("count pending reviews: %w", err)
	}

	if err := s.addDeathProgress(ctx, playerID, stats); err != nil {
		return nil, err
	}
	if err := s.addCommonMistakes(ctx, playerID, stats); err != nil {
		return nil, err
	}
	if err := s.addChampionPerformance(ctx, playerID, stats); err != nil {
		return nil, err
	}
	return stats, nil
}

func (s *Store) addDeathProgress(ctx context.Context, playerID int64, stats *model.DashboardStats) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT pms.deaths
		FROM matches m
		JOIN player_match_stats pms ON pms.match_id = m.id
		WHERE pms.player_id = ? AND m.is_remake = 0
		  AND m.queue_id IN (400, 420, 440)
		ORDER BY m.game_start_at DESC, m.id DESC
		LIMIT 20
	`, playerID)
	if err != nil {
		return fmt.Errorf("load death trend: %w", err)
	}
	var deaths []int
	for rows.Next() {
		var value int
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return fmt.Errorf("scan death trend: %w", err)
		}
		deaths = append(deaths, value)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close death trend: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate death trend: %w", err)
	}

	// Use the largest equal adjacent windows up to ten games. Equal windows keep
	// comparisons honest even when the 20-match demo includes one excluded remake.
	window := min(10, len(deaths)/2)
	if window < 2 {
		return nil
	}
	latest := meanInts(deaths[:window])
	previous := meanInts(deaths[window : window*2])
	stats.LatestDeathsAverage = &latest
	stats.PreviousDeathsAverage = &previous
	stats.ProgressWindowGames = window

	switch {
	case latest < previous-0.05:
		stats.ProgressText = fmt.Sprintf(
			"Deaths improved from %.1f to %.1f across adjacent %d-game windows.",
			previous, latest, window,
		)
	case latest > previous+0.05:
		stats.ProgressText = fmt.Sprintf(
			"Deaths moved from %.1f to %.1f across adjacent %d-game windows.",
			previous, latest, window,
		)
	default:
		stats.ProgressText = fmt.Sprintf(
			"Deaths held steady at %.1f across adjacent %d-game windows.",
			math.Round(latest*10)/10, window,
		)
	}
	return nil
}

func (s *Store) addCommonMistakes(ctx context.Context, playerID int64, stats *model.DashboardStats) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mc.id, mc.slug, mc.label, mc.is_active, mc.is_custom, COUNT(*)
		FROM review_annotations ra
		JOIN mistake_categories mc ON mc.id = ra.category_id
		JOIN match_reviews mr ON mr.id = ra.review_id
		JOIN matches m ON m.id = mr.match_id
		WHERE mr.player_id = ? AND mr.completed_at IS NOT NULL
		  AND m.queue_id IN (400, 420, 440)
		GROUP BY mc.id, mc.slug, mc.label, mc.is_active, mc.is_custom
		ORDER BY COUNT(*) DESC, mc.id
		LIMIT 5
	`, playerID)
	if err != nil {
		return fmt.Errorf("load common mistakes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var count model.CategoryCount
		var active, custom int
		if err := rows.Scan(
			&count.Category.ID, &count.Category.Slug, &count.Category.Label,
			&active, &custom, &count.Count,
		); err != nil {
			return fmt.Errorf("scan common mistake: %w", err)
		}
		count.Category.IsActive = active == 1
		count.Category.IsCustom = custom == 1
		stats.CommonMistakes = append(stats.CommonMistakes, count)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate common mistakes: %w", err)
	}
	return nil
}

func (s *Store) addChampionPerformance(ctx context.Context, playerID int64, stats *model.DashboardStats) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT pms.champion_name, COUNT(*), SUM(pms.win), AVG(pms.deaths)
		FROM matches m
		JOIN player_match_stats pms ON pms.match_id = m.id
		WHERE pms.player_id = ? AND m.is_remake = 0
		  AND m.queue_id IN (400, 420, 440)
		GROUP BY pms.champion_name
		ORDER BY COUNT(*) DESC, pms.champion_name
		LIMIT 8
	`, playerID)
	if err != nil {
		return fmt.Errorf("load champion performance: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var performance model.ChampionPerformance
		if err := rows.Scan(
			&performance.Champion, &performance.Games, &performance.Wins,
			&performance.AverageDeaths,
		); err != nil {
			return fmt.Errorf("scan champion performance: %w", err)
		}
		stats.ByChampion = append(stats.ByChampion, performance)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate champion performance: %w", err)
	}
	return nil
}

func meanInts(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	var total int
	for _, value := range values {
		total += value
	}
	return float64(total) / float64(len(values))
}
