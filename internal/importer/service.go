package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"journalol/internal/model"
	"journalol/internal/riot"
	"journalol/internal/store"
)

var (
	ErrSyncInProgress  = errors.New("a Riot sync is already running")
	ErrProfileConflict = errors.New("configured Riot account does not match the saved profile")
)

const maxOverlappingDiscovery = 1000

type riotAPI interface {
	AccountByRiotID(context.Context, riot.RegionalRoute, string, string) (riot.Account, error)
	MatchIDsForQueue(context.Context, riot.RegionalRoute, string, int, int, int) ([]string, error)
	MatchDetail(context.Context, riot.RegionalRoute, string) (riot.MatchPayload, error)
	Timeline(context.Context, riot.RegionalRoute, string) (riot.TimelinePayload, error)
}

// Settings identify the one account owned by this local Journalol instance.
type Settings struct {
	GameName      string
	TagLine       string
	PlatformRoute string
	RegionalRoute string
	HistoryLimit  int
	PollInterval  time.Duration
	Location      *time.Location
}

// Service coordinates one serialized Riot import pipeline.
type Service struct {
	store    *store.Store
	client   riotAPI
	settings Settings
	route    riot.RegionalRoute
	logger   *slog.Logger
	mu       sync.Mutex
	running  bool
}

func NewService(
	dataStore *store.Store,
	client riotAPI,
	settings Settings,
	logger *slog.Logger,
) (*Service, error) {
	if dataStore == nil || client == nil {
		return nil, errors.New("Riot importer needs a store and API client")
	}
	settings.GameName = strings.TrimSpace(settings.GameName)
	settings.TagLine = strings.TrimSpace(settings.TagLine)
	settings.PlatformRoute = strings.ToUpper(strings.TrimSpace(settings.PlatformRoute))
	settings.RegionalRoute = strings.ToUpper(strings.TrimSpace(settings.RegionalRoute))
	if settings.GameName == "" || settings.TagLine == "" || settings.PlatformRoute == "" {
		return nil, errors.New("Riot importer needs a Riot ID and platform route")
	}
	route, err := riot.ParseRegionalRoute(settings.RegionalRoute)
	if err != nil {
		return nil, err
	}
	if settings.HistoryLimit < 1 || settings.HistoryLimit > 100 {
		settings.HistoryLimit = 20
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:    dataStore,
		client:   client,
		settings: settings,
		route:    route,
		logger:   logger,
	}, nil
}

// EnsureProfile resolves the configured Riot ID and creates or refreshes the
// local primary profile. It never silently changes an existing profile to a
// different PUUID because doing so would misattribute journal history.
func (s *Service) EnsureProfile(ctx context.Context) (*model.PlayerProfile, error) {
	account, err := s.client.AccountByRiotID(
		ctx,
		s.route,
		s.settings.GameName,
		s.settings.TagLine,
	)
	if err != nil {
		return nil, err
	}
	account.PUUID = strings.TrimSpace(account.PUUID)
	account.GameName = strings.TrimSpace(account.GameName)
	account.TagLine = strings.TrimSpace(account.TagLine)
	if account.PUUID == "" {
		return nil, fmt.Errorf("%w: Riot account response did not contain a PUUID", ErrInvalidMatch)
	}
	if account.GameName == "" {
		account.GameName = s.settings.GameName
	}
	if account.TagLine == "" {
		account.TagLine = s.settings.TagLine
	}

	existing, err := s.store.PrimaryPlayer(ctx)
	switch {
	case err == nil:
		if existing.IsDemo {
			return nil, fmt.Errorf(
				"%w: the current database contains demo data; use a fresh database for Riot data",
				ErrProfileConflict,
			)
		}
		if existing.PUUID != account.PUUID {
			return nil, fmt.Errorf(
				"%w: configured Riot ID resolves to a different PUUID",
				ErrProfileConflict,
			)
		}
	case errors.Is(err, store.ErrNotFound):
		existing = &model.PlayerProfile{}
	default:
		return nil, fmt.Errorf("load primary profile: %w", err)
	}

	existing.GameName = account.GameName
	existing.TagLine = account.TagLine
	existing.PlatformRoute = s.settings.PlatformRoute
	existing.RegionalRoute = string(s.route)
	existing.PUUID = account.PUUID
	existing.IsPrimary = true
	existing.IsDemo = false
	existing.HistoryLimit = s.settings.HistoryLimit
	if s.settings.PollInterval > 0 {
		existing.PollIntervalMins = max(1, int(s.settings.PollInterval/time.Minute))
	} else {
		existing.PollIntervalMins = 5
	}
	return s.store.SavePrimaryPlayer(ctx, *existing)
}

// Sync discovers Normal Draft, Ranked Solo/Duo, and Ranked Flex history,
// paging each queue until it overlaps a known match after the initial import.
// A partial run still returns nil when useful data was retained; its status is
// visible on the dashboard and incomplete jobs are retried on later runs.
func (s *Service) Sync(ctx context.Context, trigger string) (*store.SyncRun, error) {
	if !s.beginSync() {
		return nil, ErrSyncInProgress
	}
	defer s.endSync()

	existing, existingErr := s.store.PrimaryPlayer(ctx)
	player, err := s.EnsureProfile(ctx)
	if err != nil {
		if existingErr == nil && !existing.IsDemo {
			return s.recordProfileFailure(ctx, existing.ID, trigger, err)
		}
		return nil, err
	}
	run, err := s.store.StartSyncRun(ctx, store.SyncRunStart{
		PlayerID: player.ID,
		Trigger:  trigger,
	})
	if err != nil {
		return nil, err
	}

	matchIDs, discoveryErr := s.discoverMatchIDs(ctx, player)
	if discoveryErr != nil && len(matchIDs) == 0 {
		err = discoveryErr
		return s.failDiscovery(ctx, run, err)
	}
	matchIDs = uniqueMatchIDs(matchIDs)

	counts := store.SyncRunFinish{
		State:           store.SyncStateSucceeded,
		DiscoveredCount: len(matchIDs),
	}
	var firstFailure error
	if discoveryErr != nil {
		counts.FailedCount = 1
		firstFailure = discoveryErr
	}
	deferredCount := 0
	rateLimitDeferred := false
	now := time.Now().UTC()
	for _, matchID := range matchIDs {
		if err := ctx.Err(); err != nil {
			firstFailure = err
			counts.FailedCount++
			break
		}
		job, err := s.store.QueueImportJob(ctx, store.ImportJobStart{
			PlayerID:    player.ID,
			RiotMatchID: matchID,
			SyncRunID:   run.ID,
		})
		if err != nil {
			counts.FailedCount++
			firstFailure = firstError(firstFailure, err)
			continue
		}
		if job.State == store.ImportJobComplete {
			counts.SkippedCount++
		} else if job.NextAttemptAt != nil && job.NextAttemptAt.After(now) {
			deferredCount++
			if job.ErrorCode == string(riot.ErrorRateLimited) {
				rateLimitDeferred = true
			}
		}
	}

	if ctx.Err() == nil && !rateLimitDeferred {
		jobs, err := s.store.ReadyImportJobs(ctx, player.ID, 200)
		if err != nil {
			counts.FailedCount++
			firstFailure = firstError(firstFailure, err)
		} else {
			for index := range jobs {
				if err := ctx.Err(); err != nil {
					firstFailure = firstError(firstFailure, err)
					counts.FailedCount++
					break
				}
				imported, err := s.importJob(ctx, player, &jobs[index])
				if imported {
					counts.ImportedCount++
				}
				if err != nil {
					counts.FailedCount++
					firstFailure = firstError(firstFailure, err)
					if shouldStopBatch(err) {
						break
					}
				}
			}
		}
	}
	if deferredCount > 0 {
		counts.FailedCount += deferredCount
		firstFailure = firstError(
			firstFailure,
			errors.New("one or more Riot imports are waiting for their retry window"),
		)
	}

	if counts.FailedCount > 0 {
		if counts.ImportedCount > 0 || counts.SkippedCount > 0 || deferredCount > 0 {
			counts.State = store.SyncStatePartial
		} else {
			counts.State = store.SyncStateFailed
		}
		counts.ErrorCode, counts.ErrorMessage = safeError(firstFailure)
	}
	return s.finishRun(ctx, run, player.ID, counts, firstFailure)
}

func (s *Service) discoverMatchIDs(
	ctx context.Context,
	player *model.PlayerProfile,
) ([]string, error) {
	knownMatches, err := s.store.RecentMatches(ctx, player.ID, 200)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(knownMatches))
	for _, match := range knownMatches {
		known[match.RiotMatchID] = struct{}{}
	}
	hadKnownMatches := len(known) > 0

	pageSize := s.settings.HistoryLimit
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	discovered := make([]string, 0, pageSize*len(model.TrainingQueueIDs()))
	for _, queueID := range model.TrainingQueueIDs() {
		queueMatches, err := s.discoverQueueMatchIDs(
			ctx, player.PUUID, queueID, pageSize, known, hadKnownMatches,
		)
		if err != nil {
			return discovered, err
		}
		discovered = append(discovered, queueMatches...)
	}
	return uniqueMatchIDs(discovered), nil
}

func (s *Service) discoverQueueMatchIDs(
	ctx context.Context,
	puuid string,
	queueID int,
	pageSize int,
	known map[string]struct{},
	hadKnownMatches bool,
) ([]string, error) {
	discovered := make([]string, 0, pageSize)
	start := 0
	for len(discovered) < maxOverlappingDiscovery {
		count := min(pageSize, maxOverlappingDiscovery-len(discovered))
		page, err := s.client.MatchIDsForQueue(
			ctx, s.route, puuid, start, count, queueID,
		)
		if err != nil {
			return discovered, err
		}
		discovered = append(discovered, page...)

		overlapped := false
		for _, matchID := range page {
			if _, ok := known[matchID]; ok {
				overlapped = true
				break
			}
		}
		if !hadKnownMatches || overlapped || len(page) < count || len(page) == 0 {
			break
		}
		start += len(page)
	}
	return discovered, nil
}

// RunPolling blocks until ctx is canceled and performs a sync at each
// interval. Startup sync is controlled separately by configuration.
func (s *Service) RunPolling(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, err := s.Sync(ctx, store.SyncTriggerPoll)
			if errors.Is(err, ErrSyncInProgress) {
				continue
			}
			if err != nil {
				s.logger.WarnContext(ctx, "Riot poll failed", "error", err)
				continue
			}
			if run != nil {
				s.logger.InfoContext(
					ctx,
					"Riot poll finished",
					"state", run.State,
					"imported", run.ImportedCount,
					"skipped", run.SkippedCount,
					"failed", run.FailedCount,
				)
			}
		}
	}
}

func (s *Service) importJob(
	ctx context.Context,
	player *model.PlayerProfile,
	job *store.ImportJob,
) (bool, error) {
	normalized, err := s.loadOrFetchDetail(ctx, player, job)
	if err != nil {
		s.deferJob(ctx, job.ID, store.ImportResumeDetail, err)
		return false, err
	}

	if _, err := s.store.UpsertImportedMatch(
		ctx,
		toStoreMatch(player.ID, normalized, false, s.settings.Location),
	); err != nil {
		s.deferJob(ctx, job.ID, store.ImportResumeNormalizeDetail, err)
		return false, err
	}
	if _, err := s.store.UpdateImportJob(ctx, store.ImportJobUpdate{
		JobID:      job.ID,
		State:      store.ImportJobDetailOnly,
		ResumeStep: store.ImportResumeTimeline,
	}); err != nil {
		return true, err
	}

	timeline, err := s.loadOrFetchTimeline(ctx, player, job)
	if err != nil {
		s.markTimelinePartial(ctx, player.ID, job.RiotMatchID)
		s.deferTimeline(ctx, job.ID, err)
		return true, err
	}
	if err := ApplyTimeline(&normalized, timeline); err != nil {
		s.markTimelinePartial(ctx, player.ID, job.RiotMatchID)
		s.deferTimeline(ctx, job.ID, err)
		return true, err
	}
	if _, err := s.store.UpsertImportedMatch(
		ctx,
		toStoreMatch(player.ID, normalized, true, s.settings.Location),
	); err != nil {
		s.deferNormalizeTimeline(ctx, job.ID, err)
		return true, err
	}
	if _, err := s.store.UpdateImportJob(ctx, store.ImportJobUpdate{
		JobID:      job.ID,
		State:      store.ImportJobComplete,
		ResumeStep: store.ImportResumeDone,
	}); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Service) loadOrFetchDetail(
	ctx context.Context,
	player *model.PlayerProfile,
	job *store.ImportJob,
) (NormalizedMatch, error) {
	payload, err := s.store.CurrentAPIPayload(
		ctx,
		player.ID,
		job.RiotMatchID,
		store.PayloadKindMatch,
	)
	if err == nil {
		var detail riot.Match
		if err := json.Unmarshal(payload.Body, &detail); err == nil {
			normalized, normalizeErr := normalizeExpectedMatch(
				detail,
				player.PUUID,
				job.RiotMatchID,
			)
			if normalizeErr == nil {
				return normalized, nil
			}
			// Keep the invalid revision for diagnosis/re-normalization, but
			// refetch so a corrected upstream document can recover the job.
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return NormalizedMatch{}, err
	}

	if _, err := s.store.UpdateImportJob(ctx, store.ImportJobUpdate{
		JobID:            job.ID,
		State:            store.ImportJobFetchingDetail,
		ResumeStep:       store.ImportResumeDetail,
		IncrementAttempt: true,
	}); err != nil {
		return NormalizedMatch{}, err
	}
	remote, err := s.client.MatchDetail(ctx, s.route, job.RiotMatchID)
	if err != nil {
		return NormalizedMatch{}, err
	}
	if _, err := s.store.SaveAPIPayload(ctx, store.APIPayloadInput{
		PlayerID:    player.ID,
		RiotMatchID: job.RiotMatchID,
		Kind:        store.PayloadKindMatch,
		Body:        remote.Raw,
		HTTPStatus:  200,
	}); err != nil {
		return NormalizedMatch{}, err
	}
	if _, err := s.store.UpdateImportJob(ctx, store.ImportJobUpdate{
		JobID:      job.ID,
		State:      store.ImportJobDetailStored,
		ResumeStep: store.ImportResumeNormalizeDetail,
	}); err != nil {
		return NormalizedMatch{}, err
	}
	return normalizeExpectedMatch(remote.Match, player.PUUID, job.RiotMatchID)
}

func (s *Service) loadOrFetchTimeline(
	ctx context.Context,
	player *model.PlayerProfile,
	job *store.ImportJob,
) (riot.Timeline, error) {
	if job.ResumeStep == store.ImportResumeNormalizeTimeline {
		payload, err := s.store.CurrentAPIPayload(
			ctx,
			player.ID,
			job.RiotMatchID,
			store.PayloadKindTimeline,
		)
		if err == nil {
			var timeline riot.Timeline
			if err := json.Unmarshal(payload.Body, &timeline); err == nil {
				return timeline, nil
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return riot.Timeline{}, err
		}
	}

	if _, err := s.store.UpdateImportJob(ctx, store.ImportJobUpdate{
		JobID:            job.ID,
		State:            store.ImportJobFetchingTimeline,
		ResumeStep:       store.ImportResumeTimeline,
		IncrementAttempt: true,
	}); err != nil {
		return riot.Timeline{}, err
	}
	remote, err := s.client.Timeline(ctx, s.route, job.RiotMatchID)
	if err != nil {
		return riot.Timeline{}, err
	}
	if _, err := s.store.SaveAPIPayload(ctx, store.APIPayloadInput{
		PlayerID:    player.ID,
		RiotMatchID: job.RiotMatchID,
		Kind:        store.PayloadKindTimeline,
		Body:        remote.Raw,
		HTTPStatus:  200,
	}); err != nil {
		return riot.Timeline{}, err
	}
	if _, err := s.store.UpdateImportJob(ctx, store.ImportJobUpdate{
		JobID:      job.ID,
		State:      store.ImportJobFetchingTimeline,
		ResumeStep: store.ImportResumeNormalizeTimeline,
	}); err != nil {
		return riot.Timeline{}, err
	}
	return remote.Timeline, nil
}

func (s *Service) deferJob(ctx context.Context, jobID int64, resume string, cause error) {
	code, message := safeError(cause)
	persistContext, cancel := persistenceContext(ctx)
	defer cancel()
	_, _ = s.store.UpdateImportJob(persistContext, store.ImportJobUpdate{
		JobID:         jobID,
		State:         store.ImportJobRetryWait,
		ResumeStep:    resume,
		NextAttemptAt: s.nextAttempt(cause),
		ErrorCode:     code,
		ErrorMessage:  message,
	})
}

func (s *Service) deferTimeline(ctx context.Context, jobID int64, cause error) {
	code, message := safeError(cause)
	persistContext, cancel := persistenceContext(ctx)
	defer cancel()
	_, _ = s.store.UpdateImportJob(persistContext, store.ImportJobUpdate{
		JobID:         jobID,
		State:         store.ImportJobPartialTimeline,
		ResumeStep:    store.ImportResumeTimeline,
		NextAttemptAt: s.nextAttempt(cause),
		ErrorCode:     code,
		ErrorMessage:  message,
	})
}

func (s *Service) deferNormalizeTimeline(ctx context.Context, jobID int64, cause error) {
	code, message := safeError(cause)
	persistContext, cancel := persistenceContext(ctx)
	defer cancel()
	_, _ = s.store.UpdateImportJob(persistContext, store.ImportJobUpdate{
		JobID:         jobID,
		State:         store.ImportJobPartialTimeline,
		ResumeStep:    store.ImportResumeNormalizeTimeline,
		NextAttemptAt: s.nextAttempt(cause),
		ErrorCode:     code,
		ErrorMessage:  message,
	})
}

func (s *Service) nextAttempt(err error) *time.Time {
	var apiErr *riot.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != riot.ErrorRateLimited {
		return nil
	}
	delay := apiErr.RetryAfter
	if delay <= 0 {
		delay = s.settings.PollInterval
	}
	if delay < time.Minute {
		delay = time.Minute
	}
	next := time.Now().UTC().Add(delay)
	return &next
}

func (s *Service) failDiscovery(
	ctx context.Context,
	run *store.SyncRun,
	cause error,
) (*store.SyncRun, error) {
	code, message := safeError(cause)
	persistContext, cancel := persistenceContext(ctx)
	defer cancel()
	finishErr := s.store.FinishSyncRun(persistContext, run.ID, store.SyncRunFinish{
		State:        store.SyncStateFailed,
		ErrorCode:    code,
		ErrorMessage: message,
		FailedCount:  1,
	})
	if finishErr != nil {
		return run, errors.Join(cause, finishErr)
	}
	latest, err := s.store.LatestSyncRun(persistContext, *run.PlayerID)
	if err != nil {
		return run, errors.Join(cause, err)
	}
	return latest, cause
}

func (s *Service) recordProfileFailure(
	ctx context.Context,
	playerID int64,
	trigger string,
	cause error,
) (*store.SyncRun, error) {
	persistContext, cancel := persistenceContext(ctx)
	defer cancel()
	run, err := s.store.StartSyncRun(persistContext, store.SyncRunStart{
		PlayerID: playerID,
		Trigger:  trigger,
	})
	if err != nil {
		return nil, errors.Join(cause, err)
	}
	code, message := safeError(cause)
	if err := s.store.FinishSyncRun(persistContext, run.ID, store.SyncRunFinish{
		State:        store.SyncStateFailed,
		FailedCount:  1,
		ErrorCode:    code,
		ErrorMessage: message,
	}); err != nil {
		return run, errors.Join(cause, err)
	}
	latest, err := s.store.LatestSyncRun(persistContext, playerID)
	if err != nil {
		return run, errors.Join(cause, err)
	}
	return latest, cause
}

func (s *Service) finishRun(
	ctx context.Context,
	run *store.SyncRun,
	playerID int64,
	finish store.SyncRunFinish,
	cause error,
) (*store.SyncRun, error) {
	persistContext, cancel := persistenceContext(ctx)
	defer cancel()
	if err := s.store.FinishSyncRun(persistContext, run.ID, finish); err != nil {
		return run, errors.Join(cause, err)
	}
	latest, err := s.store.LatestSyncRun(persistContext, playerID)
	if err != nil {
		return run, errors.Join(cause, err)
	}
	if finish.State == store.SyncStateFailed {
		return latest, cause
	}
	return latest, nil
}

func normalizeExpectedMatch(
	detail riot.Match,
	puuid string,
	expectedMatchID string,
) (NormalizedMatch, error) {
	normalized, err := NormalizeMatch(detail, puuid)
	if err != nil {
		return NormalizedMatch{}, err
	}
	if normalized.RiotMatchID != expectedMatchID {
		return NormalizedMatch{}, fmt.Errorf(
			"%w: match detail ID does not match the requested ID",
			ErrInvalidMatch,
		)
	}
	return normalized, nil
}

func (s *Service) markTimelinePartial(
	ctx context.Context,
	playerID int64,
	riotMatchID string,
) {
	persistContext, cancel := persistenceContext(ctx)
	defer cancel()
	_ = s.store.MarkMatchTimelinePartial(
		persistContext,
		playerID,
		riotMatchID,
		NormalizerVersion,
	)
}

func persistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func toStoreMatch(
	playerID int64,
	normalized NormalizedMatch,
	replaceTimeline bool,
	location *time.Location,
) store.ImportedMatchInput {
	events := make([]store.ImportedTimelineEvent, 0, len(normalized.Events))
	for _, event := range normalized.Events {
		events = append(events, store.ImportedTimelineEvent{
			SequenceNumber:      event.SequenceNumber,
			TimestampMS:         event.TimestampMS,
			EventType:           event.EventType,
			ActorParticipantID:  event.ActorParticipantID,
			VictimParticipantID: event.VictimParticipantID,
			TeamID:              event.TeamID,
			PositionX:           event.PositionX,
			PositionY:           event.PositionY,
			DataJSON:            json.RawMessage(event.DataJSON),
		})
	}
	return store.ImportedMatchInput{
		PlayerID:          playerID,
		RiotMatchID:       normalized.RiotMatchID,
		QueueID:           normalized.QueueID,
		QueueType:         normalized.QueueType,
		MapID:             normalized.MapID,
		GameMode:          normalized.GameMode,
		GameType:          normalized.GameType,
		Patch:             normalized.Patch,
		GameStartAt:       normalized.StartedAt,
		GameEndAt:         normalized.EndedAt,
		DurationSeconds:   normalized.DurationSeconds,
		IsRemake:          normalized.IsRemake,
		Surrendered:       normalized.Surrendered,
		Completeness:      normalized.Completeness,
		NormalizerVersion: normalized.NormalizerVersion,
		TrainingLocation:  location,
		Stats: store.ImportedPlayerStats{
			ParticipantID:     normalized.Stats.ParticipantID,
			TeamID:            normalized.Stats.TeamID,
			ChampionID:        normalized.Stats.ChampionID,
			ChampionName:      normalized.Stats.ChampionName,
			Role:              normalized.Stats.Role,
			Win:               normalized.Stats.Win,
			Kills:             normalized.Stats.Kills,
			Deaths:            normalized.Stats.Deaths,
			Assists:           normalized.Stats.Assists,
			LaneMinions:       normalized.Stats.LaneMinions,
			NeutralMinions:    normalized.Stats.NeutralMinions,
			Gold:              normalized.Stats.Gold,
			ChampionDamage:    normalized.Stats.ChampionDamage,
			VisionScore:       normalized.Stats.VisionScore,
			WardsPlaced:       normalized.Stats.WardsPlaced,
			WardsKilled:       normalized.Stats.WardsKilled,
			VisionWardsBought: normalized.Stats.VisionWardsBought,
			OpponentChampion:  normalized.Stats.OpponentChampion,
			FinalItems:        normalized.Stats.FinalItems,
			Runes:             normalized.Stats.Runes,
			SummonerSpells:    normalized.Stats.SummonerSpells,
			SkillOrder:        normalized.Stats.SkillOrder,
		},
		TimelineEvents:  events,
		ReplaceTimeline: replaceTimeline,
	}
}

func uniqueMatchIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func safeError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	var apiErr *riot.APIError
	if errors.As(err, &apiErr) {
		return string(apiErr.Kind), apiErr.Error()
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled", "Riot sync was canceled"
	case errors.Is(err, ErrPlayerNotInGame), errors.Is(err, ErrInvalidMatch):
		return "invalid_match", "Riot returned match data Journalol could not normalize"
	default:
		return "import_error", "Journalol could not finish importing Riot data"
	}
}

func firstError(current, candidate error) error {
	if current != nil {
		return current
	}
	return candidate
}

func shouldStopBatch(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr *riot.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Kind {
	case riot.ErrorBadRequest,
		riot.ErrorUnauthorized,
		riot.ErrorForbidden,
		riot.ErrorRateLimited,
		riot.ErrorServer,
		riot.ErrorNetwork,
		riot.ErrorCanceled:
		return true
	default:
		return false
	}
}

func (s *Service) beginSync() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *Service) endSync() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}
