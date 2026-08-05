package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"journalol/internal/model"
)

func TestSyncRunsAndImportJobsAreDurableAndSanitized(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	player := saveImportTestPlayer(t, store)
	startedAt := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)

	run, err := store.StartSyncRun(ctx, SyncRunStart{
		PlayerID:  player.ID,
		Trigger:   SyncTriggerManual,
		StartedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("StartSyncRun(): %v", err)
	}
	if run.State != SyncStateRunning || run.PlayerID == nil ||
		*run.PlayerID != player.ID || !run.StartedAt.Equal(startedAt) {
		t.Fatalf("started run = %#v", run)
	}

	job, err := store.QueueImportJob(ctx, ImportJobStart{
		PlayerID:    player.ID,
		RiotMatchID: "NA1_12345",
		SyncRunID:   run.ID,
		QueuedAt:    startedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("QueueImportJob(): %v", err)
	}
	retryAt := time.Now().UTC().Add(-time.Minute)
	job, err = store.UpdateImportJob(ctx, ImportJobUpdate{
		JobID:            job.ID,
		State:            ImportJobRetryWait,
		ResumeStep:       ImportResumeDetail,
		IncrementAttempt: true,
		NextAttemptAt:    &retryAt,
		ErrorCode:        "rate_limit\napplication",
		ErrorMessage:     "request with RGAPI-super-secret-token failed\r\nretrying",
	})
	if err != nil {
		t.Fatalf("UpdateImportJob(): %v", err)
	}
	if job.AttemptCount != 1 || strings.Contains(job.ErrorMessage, "super-secret") ||
		strings.ContainsAny(job.ErrorMessage, "\r\n") {
		t.Fatalf("updated job was not sanitized: %#v", job)
	}

	rediscovered, err := store.QueueImportJob(ctx, ImportJobStart{
		PlayerID:    player.ID,
		RiotMatchID: "NA1_12345",
		SyncRunID:   run.ID,
	})
	if err != nil {
		t.Fatalf("rediscover QueueImportJob(): %v", err)
	}
	if rediscovered.ID != job.ID || rediscovered.State != ImportJobRetryWait ||
		rediscovered.AttemptCount != 1 {
		t.Fatalf("rediscovery reset job progress: %#v", rediscovered)
	}

	ready, err := store.ReadyImportJobs(ctx, player.ID, 10)
	if err != nil {
		t.Fatalf("ReadyImportJobs(): %v", err)
	}
	if len(ready) != 1 || ready[0].ID != job.ID {
		t.Fatalf("ready jobs = %#v, want job %d", ready, job.ID)
	}

	completedAt := startedAt.Add(30 * time.Second)
	if err := store.FinishSyncRun(ctx, run.ID, SyncRunFinish{
		State:           SyncStatePartial,
		DiscoveredCount: 2,
		ImportedCount:   1,
		FailedCount:     1,
		ErrorCode:       "upstream",
		ErrorMessage:    "RGAPI-another-secret was rejected\nby Riot",
		CompletedAt:     completedAt,
	}); err != nil {
		t.Fatalf("FinishSyncRun(): %v", err)
	}
	latest, err := store.LatestSyncRun(ctx, player.ID)
	if err != nil {
		t.Fatalf("LatestSyncRun(): %v", err)
	}
	if latest.State != SyncStatePartial || latest.DiscoveredCount != 2 ||
		latest.ImportedCount != 1 || latest.FailedCount != 1 ||
		latest.CompletedAt == nil || !latest.CompletedAt.Equal(completedAt) ||
		strings.Contains(latest.ErrorMessage, "another-secret") {
		t.Fatalf("latest run = %#v", latest)
	}
	if err := store.FinishSyncRun(ctx, run.ID, SyncRunFinish{
		State: SyncStateSucceeded,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("second FinishSyncRun() error = %v, want ErrInvalidInput", err)
	}
}

func TestAPIPayloadsAreGzippedDeduplicatedAndRevisioned(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	player := saveImportTestPlayer(t, store)
	job, err := store.QueueImportJob(ctx, ImportJobStart{
		PlayerID:    player.ID,
		RiotMatchID: "NA1_67890",
	})
	if err != nil {
		t.Fatalf("QueueImportJob(): %v", err)
	}

	firstBody := []byte(`{"metadata":{"matchId":"NA1_67890"},"version":1}`)
	first, err := store.SaveAPIPayload(ctx, APIPayloadInput{
		PlayerID:    player.ID,
		RiotMatchID: "NA1_67890",
		Kind:        PayloadKindMatch,
		Body:        firstBody,
		HTTPStatus:  200,
	})
	if err != nil {
		t.Fatalf("first SaveAPIPayload(): %v", err)
	}
	if first.Revision != 1 || !first.IsCurrent || !bytes.Equal(first.Body, firstBody) {
		t.Fatalf("first payload = %#v", first)
	}

	var encoding string
	var compressed []byte
	if err := store.db.QueryRowContext(ctx, `
		SELECT content_encoding, payload FROM api_payloads WHERE id = ?
	`, first.ID).Scan(&encoding, &compressed); err != nil {
		t.Fatalf("read compressed payload: %v", err)
	}
	if encoding != "gzip" || len(compressed) < 2 ||
		compressed[0] != 0x1f || compressed[1] != 0x8b ||
		bytes.Equal(compressed, firstBody) {
		t.Fatalf("stored encoding/body = %q/%x, want gzip bytes", encoding, compressed)
	}

	duplicate, err := store.SaveAPIPayload(ctx, APIPayloadInput{
		PlayerID:    player.ID,
		RiotMatchID: "NA1_67890",
		Kind:        PayloadKindMatch,
		Body:        append([]byte(nil), firstBody...),
		HTTPStatus:  200,
	})
	if err != nil {
		t.Fatalf("duplicate SaveAPIPayload(): %v", err)
	}
	if duplicate.ID != first.ID || duplicate.Revision != 1 {
		t.Fatalf("duplicate payload = %#v, want revision 1 ID %d", duplicate, first.ID)
	}

	secondBody := []byte(`{"metadata":{"matchId":"NA1_67890"},"version":2}`)
	second, err := store.SaveAPIPayload(ctx, APIPayloadInput{
		PlayerID:    player.ID,
		RiotMatchID: "NA1_67890",
		Kind:        PayloadKindMatch,
		Body:        secondBody,
		HTTPStatus:  200,
	})
	if err != nil {
		t.Fatalf("changed SaveAPIPayload(): %v", err)
	}
	if second.Revision != 2 || second.ID == first.ID {
		t.Fatalf("second payload = %#v, want new revision", second)
	}
	current, err := store.CurrentAPIPayload(
		ctx,
		player.ID,
		"NA1_67890",
		PayloadKindMatch,
	)
	if err != nil {
		t.Fatalf("CurrentAPIPayload(): %v", err)
	}
	if current.ID != second.ID || !bytes.Equal(current.Body, secondBody) {
		t.Fatalf("current payload = %#v, want second body", current)
	}

	var revisions, currentCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(is_current)
		FROM api_payloads
		WHERE import_job_id = ? AND payload_kind = 'match'
	`, job.ID).Scan(&revisions, &currentCount); err != nil {
		t.Fatalf("count payload revisions: %v", err)
	}
	if revisions != 2 || currentCount != 1 {
		t.Fatalf("payload revisions/current = %d/%d, want 2/1", revisions, currentCount)
	}
}

func TestUpsertImportedMatchIsIdempotentAndPreservesJournalData(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	player := saveImportTestPlayer(t, store)
	threshold := 5.0
	block, err := store.CreateAndActivateTrainingBlockAt(
		ctx,
		model.CreateTrainingBlockParams{
			PlayerID:  player.ID,
			Name:      "Intentional deaths",
			StartDate: "2026-07-01",
			Targets: []model.TrainingTargetInput{{
				Type:        model.TargetAutomatic,
				Label:       "At most five deaths",
				MetricKey:   "deaths",
				Comparator:  "<=",
				Threshold:   &threshold,
				Aggregation: "per_game",
				WindowGames: 1,
			}},
		},
		false,
		time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC),
		time.UTC,
	)
	if err != nil {
		t.Fatalf("CreateAndActivateTrainingBlockAt(): %v", err)
	}

	input := importedMatchFixture(player.ID)
	matchID, err := store.UpsertImportedMatch(ctx, input)
	if err != nil {
		t.Fatalf("detail UpsertImportedMatch(): %v", err)
	}
	detail, err := store.GetMatch(ctx, matchID)
	if err != nil {
		t.Fatalf("GetMatch(): %v", err)
	}
	if detail.Completeness != MatchCompletenessDetailOnly ||
		detail.TrainingBlockID == nil || *detail.TrainingBlockID != block.ID {
		t.Fatalf("detail import/assignment = %#v", detail.Match)
	}

	review, err := store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:        matchID,
		PlayerID:       player.ID,
		GradeScale:     model.GradeNumeric,
		Grade:          "4",
		BiggestMistake: "Facechecked before the objective.",
		Complete:       true,
	})
	if err != nil {
		t.Fatalf("UpsertReview(): %v", err)
	}

	input.Completeness = MatchCompletenessComplete
	input.ReplaceTimeline = true
	input.Stats.Deaths = 7
	input.Stats.SkillOrder = []int{1, 3, 2}
	input.TimelineEvents = []ImportedTimelineEvent{
		{
			SequenceNumber:     10,
			TimestampMS:        90_000,
			EventType:          "SKILL_LEVEL_UP",
			ActorParticipantID: intPointer(7),
			DataJSON:           []byte(`{"skillSlot":1}`),
		},
		{
			SequenceNumber:      11,
			TimestampMS:         120_000,
			EventType:           "CHAMPION_KILL",
			ActorParticipantID:  intPointer(7),
			VictimParticipantID: intPointer(2),
		},
	}
	reimportedID, err := store.UpsertImportedMatch(ctx, input)
	if err != nil {
		t.Fatalf("complete UpsertImportedMatch(): %v", err)
	}
	if reimportedID != matchID {
		t.Fatalf("reimported match ID = %d, want %d", reimportedID, matchID)
	}

	detail, err = store.GetMatch(ctx, matchID)
	if err != nil {
		t.Fatalf("GetMatch() after reimport: %v", err)
	}
	if detail.Review == nil || detail.Review.ID != review.ID ||
		detail.TrainingBlockID == nil || *detail.TrainingBlockID != block.ID ||
		detail.Completeness != MatchCompletenessComplete ||
		detail.Deaths != 7 || len(detail.SkillOrder) != 3 {
		t.Fatalf("reimported detail lost journal/import data: %#v", detail)
	}

	var matchCount, reviewCount, assignmentCount, eventCount int
	var targetState string
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM matches WHERE riot_match_id = ?",
		input.RiotMatchID,
	).Scan(&matchCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM match_reviews WHERE match_id = ?",
		matchID,
	).Scan(&reviewCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM match_training_blocks WHERE match_id = ? AND player_id = ?",
		matchID, player.ID,
	).Scan(&assignmentCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM timeline_events WHERE match_id = ?",
		matchID,
	).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT tr.result_state
		FROM target_results tr
		JOIN training_targets tt ON tt.id = tr.target_id
		WHERE tt.block_id = ? AND tr.match_id = ? AND tr.is_current = 1
	`, block.ID, matchID).Scan(&targetState); err != nil {
		t.Fatalf("read recomputed target result: %v", err)
	}
	if matchCount != 1 || reviewCount != 1 || assignmentCount != 1 ||
		eventCount != 2 || targetState != "missed" {
		t.Fatalf(
			"match/review/assignment/events/target = %d/%d/%d/%d/%q",
			matchCount,
			reviewCount,
			assignmentCount,
			eventCount,
			targetState,
		)
	}

	input.Completeness = MatchCompletenessDetailOnly
	input.ReplaceTimeline = false
	input.TimelineEvents = nil
	input.Stats.Deaths = 4
	if _, err := store.UpsertImportedMatch(ctx, input); err != nil {
		t.Fatalf("repeat detail UpsertImportedMatch(): %v", err)
	}
	if err := store.MarkMatchTimelinePartial(
		ctx,
		player.ID,
		input.RiotMatchID,
		2,
	); err != nil {
		t.Fatalf("MarkMatchTimelinePartial(): %v", err)
	}
	var completeness string
	if err := store.db.QueryRowContext(ctx, `
		SELECT completeness FROM matches WHERE id = ?
	`, matchID).Scan(&completeness); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM timeline_events WHERE match_id = ?",
		matchID,
	).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if completeness != MatchCompletenessComplete || eventCount != 2 {
		t.Fatalf("detail/partial degraded complete timeline: %q/%d", completeness, eventCount)
	}
	detail, err = store.GetMatch(ctx, matchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.SkillOrder) != 3 {
		t.Fatalf("detail-only reimport cleared timeline-derived skill order: %v", detail.SkillOrder)
	}
}

func TestImportedMatchAssignmentUsesConfiguredTimezone(t *testing.T) {
	t.Parallel()
	dataStore, _ := openTestStore(t)
	ctx := context.Background()
	player := saveImportTestPlayer(t, dataStore)
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	threshold := 5.0
	block, err := dataStore.CreateAndActivateTrainingBlockAt(
		ctx,
		model.CreateTrainingBlockParams{
			PlayerID:  player.ID,
			Name:      "Local calendar focus",
			StartDate: "2026-07-15",
			Targets: []model.TrainingTargetInput{{
				Type:        model.TargetAutomatic,
				Label:       "At most five deaths",
				MetricKey:   "deaths",
				Comparator:  "<=",
				Threshold:   &threshold,
				Aggregation: "per_game",
				WindowGames: 1,
			}},
		},
		false,
		time.Date(2026, time.July, 15, 12, 0, 0, 0, location),
		location,
	)
	if err != nil {
		t.Fatal(err)
	}

	input := importedMatchFixture(player.ID)
	input.RiotMatchID = "NA1_timezone"
	// July 16 in UTC is still July 15 in the configured Pacific timezone.
	input.GameStartAt = time.Date(2026, time.July, 16, 6, 30, 0, 0, time.UTC)
	input.GameEndAt = input.GameStartAt.Add(30 * time.Minute)
	input.TrainingLocation = location
	matchID, err := dataStore.UpsertImportedMatch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := dataStore.GetMatch(ctx, matchID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TrainingBlockID == nil || *detail.TrainingBlockID != block.ID {
		t.Fatalf("timezone-aware assignment = %v, want block %d", detail.TrainingBlockID, block.ID)
	}
}

func TestLateImportedMatchCanJoinCompletedBlock(t *testing.T) {
	t.Parallel()
	dataStore, _ := openTestStore(t)
	ctx := context.Background()
	player := saveImportTestPlayer(t, dataStore)
	threshold := 5.0
	target := []model.TrainingTargetInput{{
		Type:        model.TargetAutomatic,
		Label:       "At most five deaths",
		MetricKey:   "deaths",
		Comparator:  "<=",
		Threshold:   &threshold,
		Aggregation: "per_game",
		WindowGames: 1,
	}}
	oldBlock, err := dataStore.CreateAndActivateTrainingBlockAt(
		ctx,
		model.CreateTrainingBlockParams{
			PlayerID:  player.ID,
			Name:      "Earlier focus",
			StartDate: "2026-07-01",
			Targets:   target,
		},
		false,
		time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC),
		time.UTC,
	)
	if err != nil {
		t.Fatal(err)
	}
	newBlock, err := dataStore.CreateTrainingBlock(ctx, model.CreateTrainingBlockParams{
		PlayerID:  player.ID,
		Name:      "New focus",
		StartDate: "2026-07-20",
		Targets:   target,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.ActivateTrainingBlockAt(
		ctx,
		newBlock.ID,
		true,
		time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC),
		time.UTC,
	); err != nil {
		t.Fatal(err)
	}

	input := importedMatchFixture(player.ID)
	input.RiotMatchID = "NA1_late"
	input.GameStartAt = time.Date(2026, time.July, 15, 20, 0, 0, 0, time.UTC)
	input.GameEndAt = input.GameStartAt.Add(30 * time.Minute)
	matchID, err := dataStore.UpsertImportedMatch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := dataStore.GetMatch(ctx, matchID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TrainingBlockID == nil || *detail.TrainingBlockID != oldBlock.ID {
		t.Fatalf("late assignment = %v, want completed block %d",
			detail.TrainingBlockID, oldBlock.ID)
	}
}

func saveImportTestPlayer(t *testing.T, store *Store) *model.PlayerProfile {
	t.Helper()
	player, err := store.SavePrimaryPlayer(context.Background(), model.PlayerProfile{
		GameName:         "ImportTester",
		TagLine:          "NA1",
		PlatformRoute:    "NA1",
		RegionalRoute:    "AMERICAS",
		PUUID:            "import-test-puuid",
		PollIntervalMins: 5,
		HistoryLimit:     20,
	})
	if err != nil {
		t.Fatalf("SavePrimaryPlayer(): %v", err)
	}
	return player
}

func importedMatchFixture(playerID int64) ImportedMatchInput {
	start := time.Date(2026, time.July, 15, 19, 30, 0, 0, time.UTC)
	return ImportedMatchInput{
		PlayerID:          playerID,
		RiotMatchID:       "NA1_24680",
		QueueID:           420,
		QueueType:         "Ranked Solo",
		MapID:             11,
		GameMode:          "CLASSIC",
		GameType:          "MATCHED_GAME",
		Patch:             "26.14",
		GameStartAt:       start,
		GameEndAt:         start.Add(31 * time.Minute),
		DurationSeconds:   31 * 60,
		Completeness:      MatchCompletenessDetailOnly,
		NormalizerVersion: 1,
		Stats: ImportedPlayerStats{
			ParticipantID:     7,
			TeamID:            200,
			ChampionID:        267,
			ChampionName:      "Nami",
			Role:              "UTILITY",
			Win:               true,
			Kills:             2,
			Deaths:            3,
			Assists:           18,
			LaneMinions:       24,
			Gold:              8_900,
			ChampionDamage:    7_500,
			VisionScore:       62,
			WardsPlaced:       29,
			WardsKilled:       7,
			VisionWardsBought: 3,
			OpponentChampion:  "Thresh",
			FinalItems:        []int{3850, 1001, 2055},
			Runes:             []int{8465, 8463},
			SummonerSpells:    []int{4, 14},
		},
	}
}

func intPointer(value int) *int {
	return &value
}
