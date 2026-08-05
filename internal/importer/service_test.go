package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"journalol/internal/model"
	"journalol/internal/riot"
	"journalol/internal/store"
)

func TestServiceSyncImportsOnceAndSkipsRediscovery(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })

	api := completeFakeAPI(t)
	service, err := NewService(dataStore, api, Settings{
		GameName:      "Coach Cat",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
		HistoryLimit:  20,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.Sync(ctx, store.SyncTriggerStartup)
	if err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	if first.State != store.SyncStateSucceeded ||
		first.DiscoveredCount != 1 ||
		first.ImportedCount != 1 ||
		first.SkippedCount != 0 {
		t.Fatalf("first run = %#v", first)
	}
	if !reflect.DeepEqual(api.matchIDQueues, []int{
		model.QueueNormalDraft, model.QueueRankedSolo, model.QueueRankedFlex,
	}) {
		t.Fatalf("match discovery queues = %v", api.matchIDQueues)
	}

	player, err := dataStore.PrimaryPlayer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if player.PUUID != "primary-puuid" || player.IsDemo {
		t.Fatalf("player = %#v", player)
	}
	matches, err := dataStore.RecentMatches(ctx, player.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Completeness != store.MatchCompletenessComplete {
		t.Fatalf("matches = %#v", matches)
	}
	detail, err := dataStore.GetMatch(ctx, matches[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(detail.SkillOrder, []int{2}) {
		t.Fatalf("skill order = %v", detail.SkillOrder)
	}
	raw, err := dataStore.CurrentAPIPayload(
		ctx,
		player.ID,
		"NA1_900000001",
		store.PayloadKindMatch,
	)
	if err != nil || !json.Valid(raw.Body) {
		t.Fatalf("raw payload = %#v, error = %v", raw, err)
	}

	second, err := service.Sync(ctx, store.SyncTriggerManual)
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if second.ImportedCount != 0 || second.SkippedCount != 1 {
		t.Fatalf("second run = %#v", second)
	}
	if api.detailCalls != 1 || api.timelineCalls != 1 {
		t.Fatalf("API detail/timeline calls = %d/%d, want 1/1",
			api.detailCalls, api.timelineCalls)
	}
}

func TestServiceKeepsDetailWhenTimelineFailsThenRecovers(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })

	api := completeFakeAPI(t)
	api.timelineErr = &riot.APIError{
		Operation:  "match timeline",
		Kind:       riot.ErrorNotFound,
		StatusCode: 404,
	}
	service, err := NewService(dataStore, api, Settings{
		GameName:      "Coach Cat",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
		HistoryLimit:  20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	partial, err := service.Sync(ctx, store.SyncTriggerStartup)
	if err != nil {
		t.Fatalf("partial Sync() returned fatal error = %v", err)
	}
	if partial.State != store.SyncStatePartial ||
		partial.ImportedCount != 1 ||
		partial.FailedCount != 1 {
		t.Fatalf("partial run = %#v", partial)
	}
	player, _ := dataStore.PrimaryPlayer(ctx)
	matches, err := dataStore.RecentMatches(ctx, player.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 ||
		matches[0].Completeness != store.MatchCompletenessPartialTimeline {
		t.Fatalf("partial matches = %#v", matches)
	}

	api.timelineErr = nil
	api.matchIDs = nil
	recovered, err := service.Sync(ctx, store.SyncTriggerManual)
	if err != nil {
		t.Fatalf("recovery Sync() error = %v", err)
	}
	if recovered.State != store.SyncStateSucceeded || recovered.ImportedCount != 1 {
		t.Fatalf("recovery run = %#v", recovered)
	}
	if recovered.DiscoveredCount != 0 {
		t.Fatalf("recovery discovered = %d, want older queued job only", recovered.DiscoveredCount)
	}
	matches, _ = dataStore.RecentMatches(ctx, player.ID, 10)
	if matches[0].Completeness != store.MatchCompletenessComplete {
		t.Fatalf("recovered completeness = %q", matches[0].Completeness)
	}
	// Detail resumed from the retained raw body instead of being fetched twice.
	if api.detailCalls != 1 || api.timelineCalls != 2 {
		t.Fatalf("API detail/timeline calls = %d/%d, want 1/2",
			api.detailCalls, api.timelineCalls)
	}
}

func TestServicePagesUntilKnownMatchBoundary(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	api := completeFakeAPI(t)
	api.matchIDs = []string{"NA1_old"}
	service, err := NewService(dataStore, api, Settings{
		GameName:      "Coach Cat",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
		HistoryLimit:  20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sync(ctx, store.SyncTriggerStartup); err != nil {
		t.Fatal(err)
	}

	api.matchIDStarts = nil
	api.matchIDs = make([]string, 0, 26)
	for index := range 25 {
		api.matchIDs = append(api.matchIDs, fmt.Sprintf("NA1_new_%02d", index))
	}
	api.matchIDs = append(api.matchIDs, "NA1_old")
	run, err := service.Sync(ctx, store.SyncTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if run.DiscoveredCount != 26 || run.ImportedCount != 25 || run.SkippedCount != 1 {
		t.Fatalf("overlap run = %#v", run)
	}
	if !reflect.DeepEqual(api.matchIDStarts, []int{0, 20}) {
		t.Fatalf("match discovery starts = %v, want [0 20]", api.matchIDStarts)
	}
}

func TestServiceHonorsRateLimitRetryWindow(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	api := completeFakeAPI(t)
	api.timelineErr = &riot.APIError{
		Operation:  "match timeline",
		Kind:       riot.ErrorRateLimited,
		StatusCode: 429,
		RetryAfter: 10 * time.Minute,
		Retryable:  true,
	}
	service, err := NewService(dataStore, api, Settings{
		GameName:      "Coach Cat",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
		HistoryLimit:  20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Sync(ctx, store.SyncTriggerStartup)
	if err != nil || first.State != store.SyncStatePartial {
		t.Fatalf("first rate-limited sync = %#v, %v", first, err)
	}
	if api.timelineCalls != 1 {
		t.Fatalf("timeline calls = %d, want 1", api.timelineCalls)
	}

	second, err := service.Sync(ctx, store.SyncTriggerManual)
	if err != nil {
		t.Fatalf("deferred Sync() error = %v", err)
	}
	if second.State != store.SyncStatePartial || second.FailedCount != 1 {
		t.Fatalf("deferred run = %#v", second)
	}
	if api.timelineCalls != 1 {
		t.Fatalf("retry window was ignored; timeline calls = %d", api.timelineCalls)
	}
}

func TestEnsureProfileRefusesDifferentSavedAccount(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	api := completeFakeAPI(t)
	service, err := NewService(dataStore, api, Settings{
		GameName:      "Coach Cat",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnsureProfile(ctx); err != nil {
		t.Fatal(err)
	}
	api.account.PUUID = "different-puuid"
	if _, err := service.EnsureProfile(ctx); !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("EnsureProfile() error = %v, want ErrProfileConflict", err)
	}
}

func TestServiceRecordsAccountCredentialFailureForDashboard(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	api := completeFakeAPI(t)
	service, err := NewService(dataStore, api, Settings{
		GameName:      "Coach Cat",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	player, err := service.EnsureProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	api.accountErr = &riot.APIError{
		Operation:  "account lookup",
		Kind:       riot.ErrorUnauthorized,
		StatusCode: 401,
	}

	run, err := service.Sync(ctx, store.SyncTriggerPoll)
	if err == nil {
		t.Fatal("Sync() error = nil, want credential failure")
	}
	if run == nil || run.State != store.SyncStateFailed ||
		run.ErrorCode != string(riot.ErrorUnauthorized) ||
		run.CompletedAt == nil {
		t.Fatalf("recorded run = %#v", run)
	}
	latest, loadErr := dataStore.LatestSyncRun(ctx, player.ID)
	if loadErr != nil || latest.ID != run.ID {
		t.Fatalf("latest run = %#v, error = %v", latest, loadErr)
	}
}

func TestServiceFinishesRunAfterContextCancellation(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	api := completeFakeAPI(t)
	service, err := NewService(dataStore, api, Settings{
		GameName:      "Coach Cat",
		TagLine:       "NA1",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	api.cancelMatchIDs = cancel
	run, err := service.Sync(ctx, store.SyncTriggerManual)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync() error = %v, want context cancellation", err)
	}
	if run == nil || run.State != store.SyncStateFailed || run.CompletedAt == nil {
		t.Fatalf("canceled run was not finalized: %#v", run)
	}
}

type fakeRiotAPI struct {
	account        riot.Account
	accountErr     error
	matchIDs       []string
	match          riot.Match
	timeline       riot.Timeline
	timelineErr    error
	detailCalls    int
	timelineCalls  int
	matchIDStarts  []int
	matchIDQueues  []int
	cancelMatchIDs context.CancelFunc
}

func completeFakeAPI(t *testing.T) *fakeRiotAPI {
	t.Helper()
	return &fakeRiotAPI{
		account: riot.Account{
			PUUID:    "primary-puuid",
			GameName: "Coach Cat",
			TagLine:  "NA1",
		},
		matchIDs: []string{"NA1_900000001", "NA1_900000001"},
		match: riot.Match{
			Metadata: riot.MatchMetadata{MatchID: "NA1_900000001"},
			Info: riot.MatchInfo{
				GameStartTimestamp: 1_700_000_000_000,
				GameDuration:       1800,
				GameVersion:        "15.14.1",
				QueueID:            420,
				MapID:              11,
				GameMode:           "CLASSIC",
				GameType:           "MATCHED_GAME",
				Participants: []riot.MatchParticipant{
					{
						PUUID:                       "primary-puuid",
						ParticipantID:               5,
						TeamID:                      100,
						ChampionID:                  40,
						ChampionName:                "Janna",
						TeamPosition:                "UTILITY",
						Win:                         true,
						Kills:                       1,
						Deaths:                      2,
						Assists:                     14,
						GoldEarned:                  9000,
						TotalDamageDealtToChampions: 8000,
						VisionScore:                 50,
						WardsPlaced:                 20,
						WardsKilled:                 4,
						VisionWardsBoughtInGame:     3,
					},
					{
						PUUID:         "enemy-puuid",
						ParticipantID: 10,
						TeamID:        200,
						ChampionName:  "Nautilus",
						TeamPosition:  "UTILITY",
					},
				},
			},
		},
		timeline: riot.Timeline{
			Metadata: riot.TimelineMetadata{MatchID: "NA1_900000001"},
			Info: riot.TimelineInfo{
				Participants: []riot.TimelineParticipant{{
					ParticipantID: 5,
					PUUID:         "primary-puuid",
				}},
				Frames: []riot.TimelineFrame{{Events: []riot.TimelineEvent{{
					Type:          "SKILL_LEVEL_UP",
					Timestamp:     60_000,
					ParticipantID: 5,
					SkillSlot:     2,
				}}}},
			},
		},
	}
}

func (f *fakeRiotAPI) AccountByRiotID(
	context.Context,
	riot.RegionalRoute,
	string,
	string,
) (riot.Account, error) {
	if f.accountErr != nil {
		return riot.Account{}, f.accountErr
	}
	return f.account, nil
}

func (f *fakeRiotAPI) MatchIDsForQueue(
	ctx context.Context,
	_ riot.RegionalRoute,
	_ string,
	start int,
	count int,
	queueID int,
) ([]string, error) {
	if f.cancelMatchIDs != nil {
		cancel := f.cancelMatchIDs
		f.cancelMatchIDs = nil
		cancel()
		return nil, ctx.Err()
	}
	f.matchIDQueues = append(f.matchIDQueues, queueID)
	if queueID != model.QueueRankedSolo {
		return []string{}, nil
	}
	f.matchIDStarts = append(f.matchIDStarts, start)
	if start >= len(f.matchIDs) {
		return []string{}, nil
	}
	end := min(len(f.matchIDs), start+count)
	return append([]string(nil), f.matchIDs[start:end]...), nil
}

func (f *fakeRiotAPI) MatchDetail(
	_ context.Context,
	_ riot.RegionalRoute,
	matchID string,
) (riot.MatchPayload, error) {
	f.detailCalls++
	detail := f.match
	detail.Metadata.MatchID = matchID
	raw, err := json.Marshal(detail)
	if err != nil {
		return riot.MatchPayload{}, err
	}
	return riot.MatchPayload{Raw: raw, Match: detail}, nil
}

func (f *fakeRiotAPI) Timeline(
	_ context.Context,
	_ riot.RegionalRoute,
	matchID string,
) (riot.TimelinePayload, error) {
	f.timelineCalls++
	if f.timelineErr != nil {
		return riot.TimelinePayload{}, f.timelineErr
	}
	timeline := f.timeline
	timeline.Metadata.MatchID = matchID
	raw, err := json.Marshal(timeline)
	if err != nil {
		return riot.TimelinePayload{}, err
	}
	return riot.TimelinePayload{Raw: raw, Timeline: timeline}, nil
}
