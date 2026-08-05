package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"journalol/internal/model"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "journalol.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return store, path
}

func TestOpenCreatesRequestedDatabaseAndMigrates(t *testing.T) {
	t.Parallel()
	store, path := openTestStore(t)
	ctx := context.Background()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping(): %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("database was not created at requested path %q: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("database path mode = %v, want regular file", info.Mode())
	}

	var foreignKeys, migrationCount, categoryCount int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrationCount != 3 {
		t.Fatalf("migration count = %d, want 3", migrationCount)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mistake_categories").Scan(&categoryCount); err != nil {
		t.Fatalf("count categories: %v", err)
	}
	if categoryCount != 9 {
		t.Fatalf("category count = %d, want 9", categoryCount)
	}
}

func TestOpenReappliesNoMigrations(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journalol.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close(): %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open(): %v", err)
	}
	defer second.Close()

	var migrationCount, categoryCount int
	if err := second.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if err := second.db.QueryRow("SELECT COUNT(*) FROM mistake_categories").Scan(&categoryCount); err != nil {
		t.Fatalf("count categories: %v", err)
	}
	if migrationCount != 3 || categoryCount != 9 {
		t.Fatalf("after reopen migrations/categories = %d/%d, want 3/9", migrationCount, categoryCount)
	}
}

func TestSeedDemoIsIdempotentAndComplete(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()

	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("first SeedDemo(): %v", err)
	}
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("second SeedDemo(): %v", err)
	}

	player, err := store.PrimaryPlayer(ctx)
	if err != nil {
		t.Fatalf("PrimaryPlayer(): %v", err)
	}
	if !player.IsDemo || player.RiotID() != "PracticePal#FOCUS" {
		t.Fatalf("player = %#v, want synthetic demo profile", player)
	}

	matches, err := store.RecentMatches(ctx, player.ID, 50)
	if err != nil {
		t.Fatalf("RecentMatches(): %v", err)
	}
	if len(matches) != 20 {
		t.Fatalf("match count = %d, want 20", len(matches))
	}
	var remakes, partial int
	for index, match := range matches {
		if index > 0 && matches[index-1].StartedAt.Before(match.StartedAt) {
			t.Fatalf("matches are not newest first at index %d", index)
		}
		if match.IsRemake {
			remakes++
		}
		if match.Completeness == "partial_timeline" {
			partial++
		}
		if match.BlockName == "" {
			t.Fatalf("match %d has no canonical block", match.ID)
		}
		if match.Role != "UTILITY" {
			t.Fatalf("match %d role = %q, want Riot-normalized UTILITY", match.ID, match.Role)
		}
	}
	if remakes != 1 || partial != 1 {
		t.Fatalf("remakes/partial = %d/%d, want 1/1", remakes, partial)
	}

	active, err := store.ActiveTrainingBlock(ctx, player.ID)
	if err != nil {
		t.Fatalf("ActiveTrainingBlock(): %v", err)
	}
	if active == nil || active.Status != model.TrainingBlockActive || len(active.Targets) != 2 {
		t.Fatalf("active block = %#v, want active with two targets", active)
	}

	stats, err := store.DashboardStats(ctx, player.ID)
	if err != nil {
		t.Fatalf("DashboardStats(): %v", err)
	}
	if stats.Games != 19 {
		t.Fatalf("eligible games = %d, want 19 (remake excluded)", stats.Games)
	}
	if stats.ProgressText == "" || stats.LatestDeathsAverage == nil || stats.PreviousDeathsAverage == nil {
		t.Fatalf("death progress is missing: %#v", stats)
	}
	if *stats.LatestDeathsAverage >= *stats.PreviousDeathsAverage {
		t.Fatalf("latest deaths %.2f did not improve over previous %.2f",
			*stats.LatestDeathsAverage, *stats.PreviousDeathsAverage)
	}
	if stats.PendingReviews != 4 {
		t.Fatalf("pending reviews = %d, want 4", stats.PendingReviews)
	}

	var profiles, storedMatches, automaticResults int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM player_profiles").Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM matches").Scan(&storedMatches); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM target_results").Scan(&automaticResults); err != nil {
		t.Fatal(err)
	}
	if profiles != 1 || storedMatches != 20 || automaticResults != 20 {
		t.Fatalf("profiles/matches/results = %d/%d/%d, want 1/20/20",
			profiles, storedMatches, automaticResults)
	}
}

func TestSeedDemoRefusesRealProfile(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()

	_, err := store.SavePrimaryPlayer(ctx, model.PlayerProfile{
		GameName:         "RealLocalPlayer",
		TagLine:          "NA1",
		PlatformRoute:    "NA1",
		RegionalRoute:    "AMERICAS",
		PUUID:            "test-real-puuid",
		PollIntervalMins: 5,
		HistoryLimit:     20,
	})
	if err != nil {
		t.Fatalf("SavePrimaryPlayer(): %v", err)
	}
	if err := store.SeedDemo(ctx); !errors.Is(err, ErrDemoProfileConflict) {
		t.Fatalf("SeedDemo() error = %v, want ErrDemoProfileConflict", err)
	}

	var matches int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM matches").Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if matches != 0 {
		t.Fatalf("demo conflict left %d matches, want 0", matches)
	}
}

func TestTrainingBlockActivationRequiresExplicitReplacement(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo(): %v", err)
	}
	player, _ := store.PrimaryPlayer(ctx)
	oldActive, _ := store.ActiveTrainingBlock(ctx, player.ID)
	threshold := 1.5
	planned, err := store.CreateTrainingBlock(ctx, model.CreateTrainingBlockParams{
		PlayerID:    player.ID,
		Name:        "Vision before action",
		Description: "Build repeatable setup timing.",
		StartDate:   time.Now().UTC().Format(time.DateOnly),
		Reminder:    "Reset before the objective timer becomes urgent.",
		Targets: []model.TrainingTargetInput{{
			Type:        model.TargetAutomatic,
			Label:       "At least 1.5 vision score per minute",
			MetricKey:   "vision_per_minute",
			Comparator:  ">=",
			Threshold:   &threshold,
			Unit:        "vision/min",
			Aggregation: "per_game",
			WindowGames: 1,
		}},
	})
	if err != nil {
		t.Fatalf("CreateTrainingBlock(): %v", err)
	}

	if _, err := store.ActivateTrainingBlock(ctx, planned.ID, false); !errors.Is(err, ErrActiveTrainingBlock) {
		t.Fatalf("ActivateTrainingBlock(false) error = %v, want ErrActiveTrainingBlock", err)
	}
	newActive, err := store.ActivateTrainingBlock(ctx, planned.ID, true)
	if err != nil {
		t.Fatalf("ActivateTrainingBlock(true): %v", err)
	}
	if newActive.ID != planned.ID || newActive.Status != model.TrainingBlockActive {
		t.Fatalf("new active block = %#v", newActive)
	}

	blocks, err := store.ListTrainingBlocks(ctx, player.ID)
	if err != nil {
		t.Fatalf("ListTrainingBlocks(): %v", err)
	}
	var oldStatus string
	var oldEndDate *string
	for _, block := range blocks {
		if block.ID == oldActive.ID {
			oldStatus = block.Status
			oldEndDate = block.EndDate
		}
	}
	if oldStatus != model.TrainingBlockCompleted || oldEndDate == nil ||
		*oldEndDate != time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("replaced block status/end = %q/%v, want completed/today", oldStatus, oldEndDate)
	}

	_, err = store.db.ExecContext(ctx, `
		UPDATE training_targets SET threshold = 2 WHERE block_id = ?
	`, newActive.ID)
	if err == nil || !strings.Contains(err.Error(), "targets are locked") {
		t.Fatalf("editing active target error = %v, want locked-target error", err)
	}
}

func TestTrainingBlockNeedsTarget(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo(): %v", err)
	}
	player, _ := store.PrimaryPlayer(ctx)
	block, err := store.CreateTrainingBlock(ctx, model.CreateTrainingBlockParams{
		PlayerID:  player.ID,
		Name:      "Unspecified experiment",
		StartDate: time.Now().UTC().Format(time.DateOnly),
	})
	if err != nil {
		t.Fatalf("CreateTrainingBlock(): %v", err)
	}
	if _, err := store.ActivateTrainingBlock(ctx, block.ID, true); !errors.Is(err, ErrTrainingBlockNeedsTarget) {
		t.Fatalf("ActivateTrainingBlock() error = %v, want ErrTrainingBlockNeedsTarget", err)
	}
}

func TestUpsertReviewReplacesAnnotationsAndFilters(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo(): %v", err)
	}
	player, _ := store.PrimaryPlayer(ctx)
	matches, _ := store.RecentMatches(ctx, player.ID, 20)
	categories, err := store.MistakeCategories(ctx)
	if err != nil {
		t.Fatalf("MistakeCategories(): %v", err)
	}
	if len(categories) < 3 {
		t.Fatalf("category count = %d, want at least 3", len(categories))
	}
	matchID := matches[0].ID

	review, err := store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:        matchID,
		PlayerID:       player.ID,
		GradeScale:     model.GradeLetter,
		Grade:          "B",
		BiggestMistake: "Walked into the lower river without checking mid.",
		DoneWell:       "Reset before dragon.",
		NextGame:       "Name missing enemies before entering fog.",
		Complete:       true,
		CategoryIDs:    []int64{categories[0].ID, categories[0].ID},
	})
	if err != nil {
		t.Fatalf("first UpsertReview(): %v", err)
	}
	if !review.Complete || len(review.Annotations) != 1 {
		t.Fatalf("first review = %#v, want complete with one deduplicated annotation", review)
	}

	eventTime := 742
	deathSequence := 2
	review, err = store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:        matchID,
		PlayerID:       player.ID,
		GradeScale:     model.GradeNumeric,
		Grade:          "4",
		BiggestMistake: "Second death started from an unsupported facecheck.",
		DoneWell:       "Kept vision around the next objective.",
		NextGame:       "Wait for the jungler before crossing river.",
		Complete:       true,
		CategoryIDs:    []int64{categories[1].ID},
		Annotations: []model.ReviewAnnotationInput{{
			CategoryID:            categories[2].ID,
			EventTimestampSeconds: &eventTime,
			DeathSequence:         &deathSequence,
			Note:                  "No teammate could follow.",
		}},
	})
	if err != nil {
		t.Fatalf("second UpsertReview(): %v", err)
	}
	if len(review.Annotations) != 2 || review.Grade != "4" {
		t.Fatalf("updated review = %#v, want two replacement annotations and grade 4", review)
	}

	detail, err := store.GetMatch(ctx, matchID)
	if err != nil {
		t.Fatalf("GetMatch(): %v", err)
	}
	if detail.Review == nil || len(detail.SelectedCategoryIDs) != 2 || !detail.ReviewComplete {
		t.Fatalf("match detail review = %#v, selected = %v", detail.Review, detail.SelectedCategoryIDs)
	}

	reviewed := true
	filtered, err := store.ListMatches(ctx, model.MatchFilter{
		PlayerID:   player.ID,
		Reviewed:   &reviewed,
		NotesQuery: "unsupported facecheck",
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("ListMatches(review/search): %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != matchID {
		t.Fatalf("filtered matches = %#v, want only %d", filtered, matchID)
	}

	_, err = store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:        matchID,
		PlayerID:       player.ID,
		Grade:          "A",
		BiggestMistake: "This update must roll back.",
		Complete:       true,
		CategoryIDs:    []int64{999999},
	})
	if err == nil {
		t.Fatal("UpsertReview() with unknown category succeeded")
	}
	afterRollback, err := store.GetMatch(ctx, matchID)
	if err != nil {
		t.Fatalf("GetMatch() after rollback: %v", err)
	}
	if afterRollback.Review.BiggestMistake != "Second death started from an unsupported facecheck." {
		t.Fatalf("failed update was not rolled back: %#v", afterRollback.Review)
	}
}

func TestCompletedReviewNeedsGradeAndReflection(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo(): %v", err)
	}
	player, _ := store.PrimaryPlayer(ctx)
	matches, _ := store.RecentMatches(ctx, player.ID, 1)

	_, err := store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:  matches[0].ID,
		PlayerID: player.ID,
		Complete: true,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty completed review error = %v, want ErrInvalidInput", err)
	}
	_, err = store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:  matches[0].ID,
		PlayerID: player.ID,
		Grade:    "A",
		Complete: true,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reflection-free completed review error = %v, want ErrInvalidInput", err)
	}
}

func TestListMatchesFiltersQueueIDs(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	player, err := store.PrimaryPlayer(ctx)
	if err != nil {
		t.Fatal(err)
	}

	draft, err := store.ListMatches(ctx, model.MatchFilter{
		PlayerID: player.ID,
		QueueIDs: []int{model.QueueNormalDraft},
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListMatches(draft): %v", err)
	}
	if len(draft) == 0 {
		t.Fatal("draft filter returned no demo matches")
	}
	for _, match := range draft {
		if match.QueueID != model.QueueNormalDraft {
			t.Fatalf("draft match queue ID = %d, want %d", match.QueueID, model.QueueNormalDraft)
		}
	}

	ranked, err := store.ListMatches(ctx, model.MatchFilter{
		PlayerID: player.ID,
		QueueIDs: []int{model.QueueRankedSolo, model.QueueRankedFlex},
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListMatches(ranked): %v", err)
	}
	if len(ranked) == 0 {
		t.Fatal("ranked filter returned no demo matches")
	}
	for _, match := range ranked {
		if match.QueueID != model.QueueRankedSolo && match.QueueID != model.QueueRankedFlex {
			t.Fatalf("ranked match queue ID = %d", match.QueueID)
		}
	}
}

func TestManualTargetCheckinRoundTripAndCanonicalValidation(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo(): %v", err)
	}
	player, _ := store.PrimaryPlayer(ctx)
	matches, err := store.RecentMatches(ctx, player.ID, 20)
	if err != nil {
		t.Fatalf("RecentMatches(): %v", err)
	}

	detail, err := store.GetMatch(ctx, matches[0].ID)
	if err != nil {
		t.Fatalf("GetMatch(): %v", err)
	}
	if detail.AssignedBlock == nil || len(detail.AssignedBlock.Targets) != 2 {
		t.Fatalf("assigned block = %#v, want two targets", detail.AssignedBlock)
	}
	if len(detail.ManualTargetCheckins) != 1 {
		t.Fatalf("manual check-ins = %#v, want one", detail.ManualTargetCheckins)
	}
	manual := detail.ManualTargetCheckins[0]
	if manual.Value != nil {
		t.Fatalf("unreviewed manual target value = %v, want unanswered", *manual.Value)
	}

	_, err = store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:        matches[0].ID,
		PlayerID:       player.ID,
		Grade:          "4",
		BiggestMistake: "Entered river before the wave was secure.",
		Complete:       true,
		ManualTargetCheckins: []model.ManualTargetCheckinInput{{
			TargetID: manual.TargetID,
			Value:    false,
			Note:     "The second river entry was unnecessary.",
		}},
	})
	if err != nil {
		t.Fatalf("UpsertReview(manual target): %v", err)
	}
	detail, err = store.GetMatch(ctx, matches[0].ID)
	if err != nil {
		t.Fatalf("GetMatch() after check-in: %v", err)
	}
	if len(detail.ManualTargetCheckins) != 1 ||
		detail.ManualTargetCheckins[0].Value == nil ||
		*detail.ManualTargetCheckins[0].Value {
		t.Fatalf("saved manual check-in = %#v, want explicit false", detail.ManualTargetCheckins)
	}
	if detail.ManualTargetCheckins[0].Note != "The second river entry was unnecessary." {
		t.Fatalf("manual check-in note = %q", detail.ManualTargetCheckins[0].Note)
	}

	var automaticTargetID int64
	for _, target := range detail.AssignedBlock.Targets {
		if target.Type == model.TargetAutomatic {
			automaticTargetID = target.ID
		}
	}
	_, err = store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:        matches[1].ID,
		PlayerID:       player.ID,
		Grade:          "3",
		BiggestMistake: "This review must roll back.",
		Complete:       true,
		ManualTargetCheckins: []model.ManualTargetCheckinInput{{
			TargetID: automaticTargetID,
			Value:    true,
		}},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("automatic target check-in error = %v, want ErrInvalidInput", err)
	}
	rolledBack, err := store.GetMatch(ctx, matches[1].ID)
	if err != nil {
		t.Fatalf("GetMatch() after rejected check-in: %v", err)
	}
	if rolledBack.Review != nil {
		t.Fatalf("rejected check-in left review %#v", rolledBack.Review)
	}

	foreignBlock, err := store.CreateTrainingBlock(ctx, model.CreateTrainingBlockParams{
		PlayerID:  player.ID,
		Name:      "Different manual focus",
		StartDate: time.Now().UTC().Format(time.DateOnly),
		Targets: []model.TrainingTargetInput{{
			Type:         model.TargetManual,
			Label:        "Different target",
			ManualPrompt: "Did this unrelated behavior happen?",
			Comparator:   "yes",
			Aggregation:  "per_game",
			WindowGames:  1,
		}},
	})
	if err != nil {
		t.Fatalf("CreateTrainingBlock(foreign target): %v", err)
	}
	_, err = store.UpsertReview(ctx, model.UpsertReviewParams{
		MatchID:        matches[2].ID,
		PlayerID:       player.ID,
		Grade:          "3",
		BiggestMistake: "This must also roll back.",
		Complete:       true,
		ManualTargetCheckins: []model.ManualTargetCheckinInput{{
			TargetID: foreignBlock.Targets[0].ID,
			Value:    true,
		}},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign target check-in error = %v, want ErrInvalidInput", err)
	}
}

func TestCreateAndActivateTrainingBlockRollsBackOnActivationFailure(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo(): %v", err)
	}
	player, _ := store.PrimaryPlayer(ctx)

	var initialBlocks int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM training_blocks").Scan(&initialBlocks); err != nil {
		t.Fatal(err)
	}
	threshold := 4.0
	params := model.CreateTrainingBlockParams{
		PlayerID:  player.ID,
		Name:      "Atomic focus",
		StartDate: time.Now().UTC().Format(time.DateOnly),
		Targets: []model.TrainingTargetInput{{
			Type:        model.TargetAutomatic,
			Label:       "At most four deaths",
			MetricKey:   "deaths",
			Comparator:  "<=",
			Threshold:   &threshold,
			Unit:        "deaths",
			Aggregation: "per_game",
			WindowGames: 1,
		}},
	}

	if _, err := store.CreateAndActivateTrainingBlock(ctx, params, false); !errors.Is(err, ErrActiveTrainingBlock) {
		t.Fatalf("CreateAndActivateTrainingBlock(false) error = %v, want ErrActiveTrainingBlock", err)
	}
	var afterConflict int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM training_blocks").Scan(&afterConflict); err != nil {
		t.Fatal(err)
	}
	if afterConflict != initialBlocks {
		t.Fatalf("active conflict left %d blocks, started with %d", afterConflict, initialBlocks)
	}

	targetless := params
	targetless.Name = "Targetless atomic focus"
	targetless.Targets = nil
	if _, err := store.CreateAndActivateTrainingBlock(ctx, targetless, true); !errors.Is(err, ErrTrainingBlockNeedsTarget) {
		t.Fatalf("targetless atomic activation error = %v, want ErrTrainingBlockNeedsTarget", err)
	}
	var afterTargetless int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM training_blocks").Scan(&afterTargetless); err != nil {
		t.Fatal(err)
	}
	if afterTargetless != initialBlocks {
		t.Fatalf("targetless failure left %d blocks, started with %d", afterTargetless, initialBlocks)
	}

	active, err := store.CreateAndActivateTrainingBlock(ctx, params, true)
	if err != nil {
		t.Fatalf("CreateAndActivateTrainingBlock(true): %v", err)
	}
	if active.Status != model.TrainingBlockActive {
		t.Fatalf("created block status = %q, want active", active.Status)
	}
	var finalBlocks int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM training_blocks").Scan(&finalBlocks); err != nil {
		t.Fatal(err)
	}
	if finalBlocks != initialBlocks+1 {
		t.Fatalf("successful atomic create has %d blocks, want %d", finalBlocks, initialBlocks+1)
	}
}

func TestConnectionPragmasSurviveReplacementConnection(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()

	connection, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn(): %v", err)
	}
	rawErr := connection.Raw(func(any) error {
		return driver.ErrBadConn
	})
	if rawErr != nil && !errors.Is(rawErr, driver.ErrBadConn) {
		t.Fatalf("discard connection: %v", rawErr)
	}
	if err := connection.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("close discarded connection: %v", err)
	}

	var foreignKeys, busyTimeout int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("replacement PRAGMA foreign_keys: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("replacement PRAGMA busy_timeout: %v", err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 {
		t.Fatalf("replacement connection foreign_keys/busy_timeout = %d/%d, want 1/5000",
			foreignKeys, busyTimeout)
	}
}

func TestLocationAwareActivationUsesLocalDateBoundaries(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("LoadLocation(): %v", err)
	}
	player, err := store.SavePrimaryPlayer(ctx, model.PlayerProfile{
		GameName:      "BoundaryTester",
		TagLine:       "LOCAL",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
		PUUID:         "boundary-test-puuid",
	})
	if err != nil {
		t.Fatalf("SavePrimaryPlayer(): %v", err)
	}

	threshold := 5.0
	target := model.TrainingTargetInput{
		Type:        model.TargetAutomatic,
		Label:       "At most five deaths",
		MetricKey:   "deaths",
		Comparator:  "<=",
		Threshold:   &threshold,
		Unit:        "deaths",
		Aggregation: "per_game",
		WindowGames: 1,
	}
	oldActive, err := store.CreateAndActivateTrainingBlockAt(
		ctx,
		model.CreateTrainingBlockParams{
			PlayerID:  player.ID,
			Name:      "Previous focus",
			StartDate: "2026-10-31",
			Targets:   []model.TrainingTargetInput{target},
		},
		false,
		time.Date(2026, time.October, 31, 12, 0, 0, 0, location),
		location,
	)
	if err != nil {
		t.Fatalf("create previous active block: %v", err)
	}

	fixtures := []struct {
		id      string
		started time.Time
		want    bool
	}{
		{
			id:      "before-local-day",
			started: time.Date(2026, time.October, 31, 23, 45, 0, 0, location),
			want:    false,
		},
		{
			id:      "early-local-day",
			started: time.Date(2026, time.November, 1, 0, 15, 0, 0, location),
			want:    true,
		},
		{
			id:      "late-local-day",
			started: time.Date(2026, time.November, 1, 23, 45, 0, 0, location),
			want:    true,
		},
		{
			id:      "after-local-day",
			started: time.Date(2026, time.November, 2, 0, 15, 0, 0, location),
			want:    false,
		},
	}
	for _, fixture := range fixtures {
		result, err := store.db.ExecContext(ctx, `
			INSERT INTO matches (
				riot_match_id, queue_id, game_start_at, game_end_at, duration_seconds,
				imported_at, updated_at
			) VALUES (?, 420, ?, ?, 1800, ?, ?)
		`, fixture.id, fixture.started.Unix(), fixture.started.Add(30*time.Minute).Unix(),
			fixture.started.Unix(), fixture.started.Unix())
		if err != nil {
			t.Fatalf("insert match %q: %v", fixture.id, err)
		}
		matchID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO player_match_stats (
				match_id, player_id, champion_name, role, win, deaths
			) VALUES (?, ?, 'Nami', 'UTILITY', 0, 3)
		`, matchID, player.ID); err != nil {
			t.Fatalf("insert stats %q: %v", fixture.id, err)
		}
	}

	endDate := "2026-11-01"
	newBlock, err := store.CreateTrainingBlock(ctx, model.CreateTrainingBlockParams{
		PlayerID:  player.ID,
		Name:      "Fallback-day focus",
		StartDate: "2026-11-01",
		EndDate:   &endDate,
		Targets:   []model.TrainingTargetInput{target},
	})
	if err != nil {
		t.Fatalf("CreateTrainingBlock(): %v", err)
	}
	activationTime := time.Date(2026, time.November, 1, 23, 30, 0, 0, location)
	if activationTime.UTC().Format(time.DateOnly) != "2026-11-02" {
		t.Fatalf("test setup UTC date = %s, want next UTC day", activationTime.UTC().Format(time.DateOnly))
	}
	if _, err := store.ActivateTrainingBlockAt(
		ctx,
		newBlock.ID,
		true,
		activationTime,
		location,
	); err != nil {
		t.Fatalf("ActivateTrainingBlockAt(): %v", err)
	}

	var oldStatus, oldEndDate string
	if err := store.db.QueryRowContext(ctx, `
		SELECT status, end_date FROM training_blocks WHERE id = ?
	`, oldActive.ID).Scan(&oldStatus, &oldEndDate); err != nil {
		t.Fatal(err)
	}
	if oldStatus != model.TrainingBlockCompleted || oldEndDate != "2026-11-01" {
		t.Fatalf("replaced block status/end = %q/%q, want completed/2026-11-01",
			oldStatus, oldEndDate)
	}

	assigned := make(map[string]bool)
	rows, err := store.db.QueryContext(ctx, `
		SELECT m.riot_match_id
		FROM match_training_blocks mtb
		JOIN matches m ON m.id = mtb.match_id
		WHERE mtb.block_id = ?
	`, newBlock.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		assigned[id] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		if assigned[fixture.id] != fixture.want {
			t.Errorf("assignment for %q = %t, want %t", fixture.id, assigned[fixture.id], fixture.want)
		}
	}
}

func TestLocationAwareActivationRejectsFutureAndExpiredWithoutResidue(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo(): %v", err)
	}
	player, _ := store.PrimaryPlayer(ctx)
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	threshold := 5.0
	target := model.TrainingTargetInput{
		Type:        model.TargetAutomatic,
		Label:       "At most five deaths",
		MetricKey:   "deaths",
		Comparator:  "<=",
		Threshold:   &threshold,
		Aggregation: "per_game",
		WindowGames: 1,
	}
	var initialBlocks int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM training_blocks").Scan(&initialBlocks); err != nil {
		t.Fatal(err)
	}

	expiredEnd := "2026-07-14"
	tests := []model.CreateTrainingBlockParams{
		{
			PlayerID:  player.ID,
			Name:      "Future focus",
			StartDate: "2026-07-16",
			Targets:   []model.TrainingTargetInput{target},
		},
		{
			PlayerID:  player.ID,
			Name:      "Expired focus",
			StartDate: "2026-07-01",
			EndDate:   &expiredEnd,
			Targets:   []model.TrainingTargetInput{target},
		},
	}
	for _, params := range tests {
		if _, err := store.CreateAndActivateTrainingBlockAt(
			ctx,
			params,
			true,
			now,
			time.UTC,
		); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("CreateAndActivateTrainingBlockAt(%q) error = %v, want ErrInvalidInput",
				params.Name, err)
		}
		var blockCount int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM training_blocks").Scan(&blockCount); err != nil {
			t.Fatal(err)
		}
		if blockCount != initialBlocks {
			t.Fatalf("%q failure left %d blocks, started with %d",
				params.Name, blockCount, initialBlocks)
		}
		active, err := store.ActiveTrainingBlock(ctx, player.ID)
		if err != nil {
			t.Fatal(err)
		}
		if active == nil || active.Name != "Safer support setup" {
			t.Fatalf("%q failure replaced active block with %#v", params.Name, active)
		}
	}

	future, err := store.CreateTrainingBlock(ctx, tests[0])
	if err != nil {
		t.Fatalf("CreateTrainingBlock(future): %v", err)
	}
	if _, err := store.ActivateTrainingBlockAt(
		ctx,
		future.ID,
		true,
		now,
		time.UTC,
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ActivateTrainingBlockAt(future) error = %v, want ErrInvalidInput", err)
	}
	blocks, err := store.ListTrainingBlocks(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		if block.ID == future.ID && block.Status != model.TrainingBlockPlanned {
			t.Fatalf("rejected future block status = %q, want planned", block.Status)
		}
	}
}

func TestResultFiltersExcludeRemakes(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo(): %v", err)
	}
	player, _ := store.PrimaryPlayer(ctx)

	loss := false
	losses, err := store.ListMatches(ctx, model.MatchFilter{
		PlayerID: player.ID,
		Result:   &loss,
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListMatches(loss): %v", err)
	}
	if len(losses) == 0 {
		t.Fatal("loss filter returned no matches")
	}
	for _, match := range losses {
		if match.IsRemake {
			t.Fatalf("loss filter included remake %q", match.RiotMatchID)
		}
		if match.Win {
			t.Fatalf("loss filter included win %q", match.RiotMatchID)
		}
	}

	win := true
	wins, err := store.ListMatches(ctx, model.MatchFilter{
		PlayerID: player.ID,
		Result:   &win,
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListMatches(win): %v", err)
	}
	for _, match := range wins {
		if match.IsRemake {
			t.Fatalf("win filter included remake %q", match.RiotMatchID)
		}
		if !match.Win {
			t.Fatalf("win filter included loss %q", match.RiotMatchID)
		}
	}
}
