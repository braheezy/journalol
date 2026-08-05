// Package capture coordinates Journalol's imported death events with the local
// League Replay API. It is used only by the host CLI, never by the web server.
package capture

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"journalol/internal/model"
	"journalol/internal/replay"
	"journalol/internal/store"
)

// DeathClipOptions describes one explicit user-requested clip batch.
type DeathClipOptions struct {
	MatchID    int64
	ReplayPath string
	OutputDir  string
	BeforeMS   int64
	AfterMS    int64
	Codec      string
	FPS        int
}

type replayAPI interface {
	Check(context.Context) error
	Game(context.Context) (replay.GameState, error)
	Playback(context.Context) (replay.PlaybackState, error)
	WaitReady(context.Context, int, time.Duration) (replay.GameState, replay.PlaybackState, error)
	PrepareRecording(context.Context, float64, time.Duration) error
	PreparePlayerCamera(context.Context, func() error) error
	Record(context.Context, replay.RecordingRequest) error
}

type cameraPreparer func(context.Context) error

// Service owns the capture use case and its persistence lifecycle.
type Service struct {
	store  *store.Store
	replay replayAPI
}

func NewService(dataStore *store.Store, replayClient *replay.Client) *Service {
	return &Service{store: dataStore, replay: replayClient}
}

// GenerateDeathClips renders one clip around each imported primary-player
// death from a replay the user opened manually.
func (s *Service) GenerateDeathClips(ctx context.Context, options DeathClipOptions) ([]model.DeathClip, error) {
	if s == nil || s.store == nil || s.replay == nil {
		return nil, errors.New("capture service is not configured")
	}
	plan, options, err := s.prepare(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := s.replay.Check(ctx); err != nil {
		return nil, fmt.Errorf("open %q in League, enable EnableReplayApi=1, then retry: %w", options.ReplayPath, err)
	}
	playback, err := s.replay.Playback(ctx)
	if err != nil {
		return nil, fmt.Errorf("read open replay duration: %w", err)
	}
	return s.render(ctx, plan, options, playback, nil)
}

type capturePlan struct {
	detail  *model.MatchDetail
	subject model.ReplaySubject
	events  []model.DeathEvent
}

func (s *Service) prepare(ctx context.Context, options DeathClipOptions) (capturePlan, DeathClipOptions, error) {
	if options.MatchID <= 0 || options.BeforeMS < 0 || options.AfterMS < 1 || options.FPS < 1 || options.FPS > 120 {
		return capturePlan{}, options, errors.New("invalid death clip options")
	}
	options.ReplayPath = strings.TrimSpace(options.ReplayPath)
	options.OutputDir = strings.TrimSpace(options.OutputDir)
	options.Codec = strings.ToLower(strings.TrimSpace(options.Codec))
	if options.ReplayPath == "" || options.OutputDir == "" || options.Codec == "" {
		return capturePlan{}, options, errors.New("replay path, output directory, and codec are required")
	}
	resolvedReplay, err := filepath.Abs(options.ReplayPath)
	if err != nil {
		return capturePlan{}, options, fmt.Errorf("resolve replay path: %w", err)
	}
	resolvedOutput, err := filepath.Abs(options.OutputDir)
	if err != nil {
		return capturePlan{}, options, fmt.Errorf("resolve clip directory: %w", err)
	}
	options.ReplayPath = filepath.Clean(resolvedReplay)
	options.OutputDir = filepath.Clean(resolvedOutput)
	info, err := os.Stat(options.ReplayPath)
	if err != nil {
		return capturePlan{}, options, fmt.Errorf("read replay file: %w", err)
	}
	if info.IsDir() || !strings.EqualFold(filepath.Ext(options.ReplayPath), ".rofl") {
		return capturePlan{}, options, errors.New("replay path must be a .rofl file")
	}

	detail, err := s.store.GetMatch(ctx, options.MatchID)
	if errors.Is(err, store.ErrNotFound) {
		return capturePlan{}, options, errors.New("match not found")
	}
	if err != nil {
		return capturePlan{}, options, fmt.Errorf("load match: %w", err)
	}
	replayMatchID := strings.ReplaceAll(strings.TrimSuffix(filepath.Base(options.ReplayPath), filepath.Ext(options.ReplayPath)), "-", "_")
	if !strings.EqualFold(replayMatchID, detail.RiotMatchID) {
		return capturePlan{}, options, fmt.Errorf(
			"replay %q belongs to %s, but Journalol match %d is %s",
			options.ReplayPath,
			replayMatchID,
			options.MatchID,
			detail.RiotMatchID,
		)
	}
	subject, err := s.store.ReplaySubjectForMatch(ctx, options.MatchID)
	if err != nil {
		return capturePlan{}, options, fmt.Errorf("load replay player: %w", err)
	}
	events, err := s.store.DeathEventsForMatch(ctx, options.MatchID)
	if err != nil {
		return capturePlan{}, options, err
	}
	if len(events) == 0 {
		return capturePlan{}, options, errors.New("this match has no imported player death events; sync its timeline before capturing clips")
	}
	if err := os.MkdirAll(options.OutputDir, 0o755); err != nil {
		return capturePlan{}, options, fmt.Errorf("create clip directory: %w", err)
	}
	return capturePlan{detail: detail, subject: subject, events: events}, options, nil
}

func (s *Service) render(
	ctx context.Context,
	plan capturePlan,
	options DeathClipOptions,
	playback replay.PlaybackState,
	prepareCamera cameraPreparer,
) ([]model.DeathClip, error) {
	lengthMS := int64(math.Floor(playback.Length * 1000))
	if lengthMS <= 0 {
		return nil, errors.New("League replay did not report a usable duration")
	}
	matchDirectory := filepath.Join(options.OutputDir, safePathComponent(plan.detail.RiotMatchID))
	if err := os.MkdirAll(matchDirectory, 0o755); err != nil {
		return nil, fmt.Errorf("create match clip directory: %w", err)
	}
	clips := make([]model.DeathClip, 0, len(plan.events))
	for index, event := range plan.events {
		start := maxInt64(0, event.TimestampMS-options.BeforeMS)
		end := minInt64(lengthMS, event.TimestampMS+options.AfterMS)
		if end <= start {
			return clips, fmt.Errorf("death %d falls outside the replay duration", index+1)
		}
		output := filepath.Join(matchDirectory, fmt.Sprintf("death-%02d.%s", index+1, options.Codec))
		stagingOutput := filepath.Join(matchDirectory, fmt.Sprintf("death-%02d.pending.%s", index+1, options.Codec))
		for _, generatedPath := range []string{stagingOutput, stagingOutput + ".tmp"} {
			if err := os.Remove(generatedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return clips, fmt.Errorf("remove stale generated clip %q: %w", generatedPath, err)
			}
		}
		defer func() {
			_ = os.Remove(stagingOutput)
			_ = os.Remove(stagingOutput + ".tmp")
		}()
		clip := model.DeathClip{
			MatchID: options.MatchID, TimelineSeq: event.SequenceNumber, DeathIndex: index + 1,
			DeathTimestamp: event.TimestampMS, StartTimestamp: start, EndTimestamp: end,
			ReplayPath: options.ReplayPath, OutputPath: output, Codec: options.Codec,
			Status: model.DeathClipRecording,
		}
		if _, err := s.store.SaveDeathClip(ctx, clip); err != nil {
			return clips, err
		}
		if err := s.replay.PrepareRecording(ctx, float64(start)/1000, 45*time.Second); err != nil {
			clip.Status = model.DeathClipFailed
			clip.ErrorMessage = err.Error()
			saveContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, saveErr := s.store.SaveDeathClip(saveContext, clip)
			cancel()
			if saveErr != nil {
				return clips, saveErr
			}
			return clips, fmt.Errorf("prepare death %d: %w", index+1, err)
		}
		if prepareCamera != nil {
			if err := prepareCamera(ctx); err != nil {
				clip.Status = model.DeathClipFailed
				clip.ErrorMessage = err.Error()
				saveContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, saveErr := s.store.SaveDeathClip(saveContext, clip)
				cancel()
				if saveErr != nil {
					return clips, saveErr
				}
				return clips, fmt.Errorf("focus camera for death %d: %w", index+1, err)
			}
		}
		var onRecordingStarted func() error
		if prepareCamera != nil {
			onRecordingStarted = func() error { return prepareCamera(ctx) }
		}
		err := s.replay.Record(ctx, replay.RecordingRequest{
			Path: stagingOutput, Codec: options.Codec,
			StartTime: float64(start) / 1000, EndTime: float64(end) / 1000,
			FramesPerSecond: options.FPS, EnforceFrameRate: false,
			OnStarted: onRecordingStarted,
		})
		if err == nil {
			outputInfo, statErr := os.Stat(stagingOutput)
			if statErr != nil || outputInfo.Size() == 0 {
				if statErr != nil {
					err = fmt.Errorf("Replay API finished but no clip was written: %w", statErr)
				} else {
					err = errors.New("Replay API finished but wrote an empty clip")
				}
			}
		}
		if err == nil {
			if renameErr := os.Rename(stagingOutput, output); renameErr != nil {
				err = fmt.Errorf("publish rendered clip: %w", renameErr)
			}
		}
		if err != nil {
			clip.Status = model.DeathClipFailed
			clip.ErrorMessage = err.Error()
			saveContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, saveErr := s.store.SaveDeathClip(saveContext, clip)
			cancel()
			if saveErr != nil {
				return clips, saveErr
			}
			return clips, fmt.Errorf("render death %d: %w", index+1, err)
		}
		clip.Status = model.DeathClipReady
		clip.ErrorMessage = ""
		saved, err := s.store.SaveDeathClip(ctx, clip)
		if err != nil {
			return clips, err
		}
		clips = append(clips, *saved)
	}
	return clips, nil
}

func safePathComponent(value string) string {
	var result strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '_', character == '-':
			result.WriteRune(character)
		default:
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "match"
	}
	return result.String()
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
