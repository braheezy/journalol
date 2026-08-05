package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"journalol/internal/model"
)

// ReplaySubjectForMatch returns the primary player's replay participant slot.
// A capture must use this exact slot instead of relying on directed camera or
// whichever unit happened to be selected when League opened the replay.
func (s *Store) ReplaySubjectForMatch(ctx context.Context, matchID int64) (model.ReplaySubject, error) {
	if matchID <= 0 {
		return model.ReplaySubject{}, fmt.Errorf("%w: match is required", ErrInvalidInput)
	}
	var subject model.ReplaySubject
	err := s.db.QueryRowContext(ctx, `
		SELECT pms.participant_id, pms.team_id, pms.champion_name
		FROM player_match_stats pms
		JOIN player_profiles player
		  ON player.id = pms.player_id
		 AND player.is_primary = 1
		WHERE pms.match_id = ?
	`, matchID).Scan(&subject.ParticipantID, &subject.TeamID, &subject.Champion)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReplaySubject{}, ErrNotFound
	}
	if err != nil {
		return model.ReplaySubject{}, fmt.Errorf("load replay subject: %w", err)
	}
	subject.Champion = strings.TrimSpace(subject.Champion)
	if subject.ParticipantID < 1 || subject.ParticipantID > 10 ||
		(subject.ParticipantID <= 5 && subject.TeamID != 100) ||
		(subject.ParticipantID > 5 && subject.TeamID != 200) ||
		subject.Champion == "" {
		return model.ReplaySubject{}, fmt.Errorf(
			"%w: match has inconsistent replay participant %d on team %d",
			ErrInvalidInput,
			subject.ParticipantID,
			subject.TeamID,
		)
	}
	return subject, nil
}

// DeathEventsForMatch returns only deaths belonging to the primary player. A
// missing complete timeline is not treated as zero deaths: callers receive an
// empty result and can check the match completeness separately.
func (s *Store) DeathEventsForMatch(ctx context.Context, matchID int64) ([]model.DeathEvent, error) {
	if matchID <= 0 {
		return nil, fmt.Errorf("%w: match is required", ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT te.sequence_number, te.timestamp_ms, te.position_x, te.position_y
		FROM timeline_events te
		JOIN player_match_stats pms
		  ON pms.match_id = te.match_id
		WHERE te.match_id = ?
		  AND te.event_type = 'CHAMPION_KILL'
		  AND te.victim_participant_id = pms.participant_id
		ORDER BY te.timestamp_ms, te.sequence_number
	`, matchID)
	if err != nil {
		return nil, fmt.Errorf("list player death events: %w", err)
	}
	defer rows.Close()

	events := make([]model.DeathEvent, 0)
	for rows.Next() {
		var event model.DeathEvent
		var x, y sql.NullInt64
		if err := rows.Scan(&event.SequenceNumber, &event.TimestampMS, &x, &y); err != nil {
			return nil, fmt.Errorf("scan player death event: %w", err)
		}
		if x.Valid {
			value := int(x.Int64)
			event.PositionX = &value
		}
		if y.Valid {
			value := int(y.Int64)
			event.PositionY = &value
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate player death events: %w", err)
	}
	return events, nil
}

// SaveDeathClip records the local capture lifecycle. It is intentionally only
// called by the host CLI; browser and MCP clients cannot write clip paths.
func (s *Store) SaveDeathClip(ctx context.Context, clip model.DeathClip) (*model.DeathClip, error) {
	if clip.MatchID <= 0 || clip.TimelineSeq < 0 || clip.DeathIndex < 1 ||
		clip.DeathTimestamp < 0 || clip.StartTimestamp < 0 || clip.EndTimestamp <= clip.StartTimestamp {
		return nil, fmt.Errorf("%w: invalid death clip timing", ErrInvalidInput)
	}
	clip.ReplayPath = strings.TrimSpace(clip.ReplayPath)
	clip.OutputPath = strings.TrimSpace(clip.OutputPath)
	clip.Codec = strings.ToLower(strings.TrimSpace(clip.Codec))
	clip.ErrorMessage = sanitizeClipError(clip.ErrorMessage)
	if clip.ReplayPath == "" || clip.OutputPath == "" || clip.Codec == "" {
		return nil, fmt.Errorf("%w: replay path, output path, and codec are required", ErrInvalidInput)
	}
	switch clip.Status {
	case model.DeathClipRecording, model.DeathClipReady, model.DeathClipFailed:
	default:
		return nil, fmt.Errorf("%w: invalid death clip status", ErrInvalidInput)
	}
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO death_clips (
			match_id, timeline_sequence, death_index, death_timestamp_ms,
			start_timestamp_ms, end_timestamp_ms, replay_path, output_path,
			codec, status, error_message, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id, timeline_sequence) DO UPDATE SET
			death_index = excluded.death_index,
			death_timestamp_ms = excluded.death_timestamp_ms,
			start_timestamp_ms = excluded.start_timestamp_ms,
			end_timestamp_ms = excluded.end_timestamp_ms,
			replay_path = excluded.replay_path,
			output_path = excluded.output_path,
			codec = excluded.codec,
			status = excluded.status,
			error_message = excluded.error_message,
			updated_at = excluded.updated_at
	`, clip.MatchID, clip.TimelineSeq, clip.DeathIndex, clip.DeathTimestamp,
		clip.StartTimestamp, clip.EndTimestamp, clip.ReplayPath, clip.OutputPath,
		clip.Codec, clip.Status, clip.ErrorMessage, now, now)
	if err != nil {
		return nil, fmt.Errorf("save death clip: %w", err)
	}
	return s.deathClip(ctx, clip.MatchID, clip.TimelineSeq)
}

func sanitizeClipError(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) > 500 {
		return value[:500] + "…"
	}
	return value
}

func (s *Store) deathClip(ctx context.Context, matchID int64, sequence int) (*model.DeathClip, error) {
	var clip model.DeathClip
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, match_id, timeline_sequence, death_index, death_timestamp_ms,
		       start_timestamp_ms, end_timestamp_ms, replay_path, output_path,
		       codec, status, error_message, created_at, updated_at
		FROM death_clips WHERE match_id = ? AND timeline_sequence = ?
	`, matchID, sequence).Scan(
		&clip.ID, &clip.MatchID, &clip.TimelineSeq, &clip.DeathIndex,
		&clip.DeathTimestamp, &clip.StartTimestamp, &clip.EndTimestamp,
		&clip.ReplayPath, &clip.OutputPath, &clip.Codec, &clip.Status,
		&clip.ErrorMessage, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get death clip: %w", err)
	}
	clip.CreatedAt = unixTime(createdAt)
	clip.UpdatedAt = unixTime(updatedAt)
	return &clip, nil
}
