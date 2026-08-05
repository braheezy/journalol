package capture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"journalol/internal/leagueconfig"
	"journalol/internal/model"
	"journalol/internal/replay"
	"journalol/internal/store"
)

func TestAutomatedCaptureOwnsLifecycleAndUsesAbsoluteClampedPath(t *testing.T) {
	t.Parallel()
	dataStore, matchID, replayPath := captureFixture(t, 590_000)
	events := make([]string, 0)
	api := &fakeReplayAPI{events: &events, playback: replay.PlaybackState{Length: 600}}
	service := &Service{store: dataStore, replay: api}
	launcher := &fakeLauncher{events: &events}
	automation := &Automation{
		service:  service,
		launcher: launcher,
		applyConfig: func(settings leagueconfig.CaptureSettings) (configLease, error) {
			events = append(events, "apply")
			if settings.Width != 1280 || settings.Height != 720 {
				t.Fatalf("capture settings = %#v", settings)
			}
			return &fakeLease{events: &events}, nil
		},
	}
	clips, err := automation.GenerateDeathClips(context.Background(), DeathClipOptions{
		MatchID: matchID, ReplayPath: replayPath, OutputDir: filepath.Join(t.TempDir(), "relative", "clips"),
		BeforeMS: 60_000, AfterMS: 15_000, Codec: "webm", FPS: 60,
	}, AutomationOptions{
		LeagueRoot: "/Applications/League of Legends.app/Contents/LoL",
		ConfigPath: "/tmp/game.cfg", StateDir: "/tmp/capture", PlatformID: "NA1",
		Region: "NA", Locale: "en_US", WindowWidth: 1280, WindowHeight: 720,
		StartupTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("GenerateDeathClips(): %v", err)
	}
	if len(clips) != 1 || clips[0].EndTimestamp != 600_000 {
		t.Fatalf("clips = %#v, want one clip clamped to 600000ms", clips)
	}
	if !filepath.IsAbs(api.recording.Path) {
		t.Fatalf("recording path = %q, want absolute", api.recording.Path)
	}
	if !strings.Contains(api.recording.Path, ".pending.webm") {
		t.Fatalf("recording path = %q, want staging path", api.recording.Path)
	}
	if _, err := os.Stat(clips[0].OutputPath); err != nil {
		t.Fatalf("published clip: %v", err)
	}
	wantEvents := []string{"access", "apply", "launch", "wait", "verify", "seek", "camera", "select", "record", "camera", "select", "stop", "restore"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("lifecycle events = %#v, want %#v", events, wantEvents)
	}
	if launcher.selectedKey != 13 {
		t.Fatalf("spectator key = %d, want W key code 13 for participant 7", launcher.selectedKey)
	}
}

func TestAutomatedCaptureRestoresAndStopsAfterRecordingFailure(t *testing.T) {
	t.Parallel()
	dataStore, matchID, replayPath := captureFixture(t, 120_000)
	events := make([]string, 0)
	api := &fakeReplayAPI{
		events: &events, playback: replay.PlaybackState{Length: 600},
		recordErr: errors.New("encoder failed"),
	}
	automation := &Automation{
		service:  &Service{store: dataStore, replay: api},
		launcher: &fakeLauncher{events: &events},
		applyConfig: func(leagueconfig.CaptureSettings) (configLease, error) {
			events = append(events, "apply")
			return &fakeLease{events: &events}, nil
		},
	}
	_, err := automation.GenerateDeathClips(context.Background(), DeathClipOptions{
		MatchID: matchID, ReplayPath: replayPath, OutputDir: t.TempDir(),
		BeforeMS: 60_000, AfterMS: 15_000, Codec: "webm", FPS: 60,
	}, AutomationOptions{
		LeagueRoot: "/Applications/League of Legends.app/Contents/LoL",
		ConfigPath: "/tmp/game.cfg", StateDir: "/tmp/capture", PlatformID: "NA1",
		Region: "NA", Locale: "en_US", WindowWidth: 1280, WindowHeight: 720,
		StartupTimeout: time.Second,
	})
	if err == nil || !errors.Is(err, api.recordErr) {
		t.Fatalf("GenerateDeathClips() error = %v, want encoder failure", err)
	}
	if got := events[len(events)-2:]; !reflect.DeepEqual(got, []string{"stop", "restore"}) {
		t.Fatalf("cleanup events = %#v, want stop then restore", got)
	}
}

func TestAutomatedCaptureChecksParticipantInputBeforeChangingSettings(t *testing.T) {
	t.Parallel()
	dataStore, matchID, replayPath := captureFixture(t, 120_000)
	events := make([]string, 0)
	want := errors.New("access denied")
	launcher := &fakeLauncher{events: &events, accessErr: want}
	automation := &Automation{
		service:  &Service{store: dataStore, replay: &fakeReplayAPI{events: &events, playback: replay.PlaybackState{Length: 600}}},
		launcher: launcher,
		applyConfig: func(leagueconfig.CaptureSettings) (configLease, error) {
			events = append(events, "apply")
			return &fakeLease{events: &events}, nil
		},
	}
	_, err := automation.GenerateDeathClips(context.Background(), DeathClipOptions{
		MatchID: matchID, ReplayPath: replayPath, OutputDir: t.TempDir(),
		BeforeMS: 60_000, AfterMS: 15_000, Codec: "webm", FPS: 60,
	}, AutomationOptions{
		LeagueRoot: "/Applications/League of Legends.app/Contents/LoL",
		ConfigPath: "/tmp/game.cfg", StateDir: "/tmp/capture", PlatformID: "NA1",
		Region: "NA", Locale: "en_US", WindowWidth: 1280, WindowHeight: 720,
		StartupTimeout: time.Second,
	})
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("GenerateDeathClips() error = %v, want access failure", err)
	}
	if !reflect.DeepEqual(events, []string{"access"}) {
		t.Fatalf("events = %#v, want access check only", events)
	}
}

func TestAutomatedCapturePreservesExistingClipWhenCameraFocusFails(t *testing.T) {
	t.Parallel()
	dataStore, matchID, replayPath := captureFixture(t, 120_000)
	events := make([]string, 0)
	outputRoot := t.TempDir()
	matchDirectory := filepath.Join(outputRoot, "NA1_24680")
	if err := os.MkdirAll(matchDirectory, 0o755); err != nil {
		t.Fatalf("create existing clip directory: %v", err)
	}
	outputPath := filepath.Join(matchDirectory, "death-01.webm")
	wantContent := []byte("previous-good-clip")
	if err := os.WriteFile(outputPath, wantContent, 0o600); err != nil {
		t.Fatalf("write existing clip: %v", err)
	}
	want := errors.New("camera unavailable")
	automation := &Automation{
		service: &Service{store: dataStore, replay: &fakeReplayAPI{
			events: &events, playback: replay.PlaybackState{Length: 600}, cameraErr: want,
		}},
		launcher: &fakeLauncher{events: &events},
		applyConfig: func(leagueconfig.CaptureSettings) (configLease, error) {
			events = append(events, "apply")
			return &fakeLease{events: &events}, nil
		},
	}
	_, err := automation.GenerateDeathClips(context.Background(), DeathClipOptions{
		MatchID: matchID, ReplayPath: replayPath, OutputDir: outputRoot,
		BeforeMS: 60_000, AfterMS: 15_000, Codec: "webm", FPS: 60,
	}, AutomationOptions{
		LeagueRoot: "/Applications/League of Legends.app/Contents/LoL",
		ConfigPath: "/tmp/game.cfg", StateDir: "/tmp/capture", PlatformID: "NA1",
		Region: "NA", Locale: "en_US", WindowWidth: 1280, WindowHeight: 720,
		StartupTimeout: time.Second,
	})
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("GenerateDeathClips() error = %v, want camera failure", err)
	}
	gotContent, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("read preserved clip: %v", readErr)
	}
	if !reflect.DeepEqual(gotContent, wantContent) {
		t.Fatalf("preserved clip = %q, want %q", gotContent, wantContent)
	}
}

type fakeReplayAPI struct {
	events    *[]string
	playback  replay.PlaybackState
	recording replay.RecordingRequest
	recordErr error
	cameraErr error
}

func (f *fakeReplayAPI) Check(context.Context) error { return nil }
func (f *fakeReplayAPI) Game(context.Context) (replay.GameState, error) {
	return replay.GameState{ProcessID: 42}, nil
}
func (f *fakeReplayAPI) Playback(context.Context) (replay.PlaybackState, error) {
	return f.playback, nil
}
func (f *fakeReplayAPI) WaitReady(context.Context, int, time.Duration) (replay.GameState, replay.PlaybackState, error) {
	*f.events = append(*f.events, "wait")
	return replay.GameState{ProcessID: 42}, f.playback, nil
}
func (f *fakeReplayAPI) PrepareRecording(context.Context, float64, time.Duration) error {
	*f.events = append(*f.events, "seek")
	return nil
}
func (f *fakeReplayAPI) PreparePlayerCamera(_ context.Context, selectPlayer func() error) error {
	*f.events = append(*f.events, "camera")
	if f.cameraErr != nil {
		return f.cameraErr
	}
	return selectPlayer()
}
func (f *fakeReplayAPI) Record(_ context.Context, request replay.RecordingRequest) error {
	*f.events = append(*f.events, "record")
	f.recording = request
	if f.recordErr != nil {
		return f.recordErr
	}
	if request.OnStarted != nil {
		if err := request.OnStarted(); err != nil {
			return err
		}
	}
	return os.WriteFile(request.Path, []byte("webm"), 0o600)
}

type fakeLauncher struct {
	events      *[]string
	selectedKey uint16
	accessErr   error
}

func (f *fakeLauncher) EnsureParticipantInput() error {
	*f.events = append(*f.events, "access")
	return f.accessErr
}

func (f *fakeLauncher) Launch(context.Context, replay.LaunchOptions) (int, error) {
	*f.events = append(*f.events, "launch")
	return 42, nil
}
func (f *fakeLauncher) VerifyOwned(context.Context, int, string) error {
	*f.events = append(*f.events, "verify")
	return nil
}
func (f *fakeLauncher) SelectParticipant(_ context.Context, _ int, _ string, virtualKey uint16) error {
	*f.events = append(*f.events, "select")
	f.selectedKey = virtualKey
	return nil
}
func (f *fakeLauncher) StopOwned(context.Context, int, string) error {
	*f.events = append(*f.events, "stop")
	return nil
}
func (f *fakeLauncher) FindSelectedReplay(context.Context, string) (int, error) {
	return 42, nil
}

type fakeLease struct {
	events *[]string
}

func (f *fakeLease) Restore() error {
	*f.events = append(*f.events, "restore")
	return nil
}

func captureFixture(t *testing.T, deathTimestamp int64) (*store.Store, int64, string) {
	t.Helper()
	directory := t.TempDir()
	dataStore, err := store.Open(filepath.Join(directory, "journalol.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := dataStore.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	player, err := dataStore.SavePrimaryPlayer(context.Background(), model.PlayerProfile{
		GameName: "Capture", TagLine: "NA1", PlatformRoute: "NA1", RegionalRoute: "AMERICAS", PUUID: "capture-puuid",
	})
	if err != nil {
		t.Fatalf("save player: %v", err)
	}
	participantID := 7
	startedAt := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	matchID, err := dataStore.UpsertImportedMatch(context.Background(), store.ImportedMatchInput{
		PlayerID: player.ID, RiotMatchID: "NA1_24680", QueueID: model.QueueRankedSolo,
		QueueType: "Ranked Solo", MapID: 11, GameMode: "CLASSIC", GameType: "MATCHED_GAME",
		Patch: "26.15", GameStartAt: startedAt, GameEndAt: startedAt.Add(10 * time.Minute),
		DurationSeconds: 600, Completeness: store.MatchCompletenessComplete, NormalizerVersion: 1,
		ReplaceTimeline: true,
		Stats: store.ImportedPlayerStats{
			ParticipantID: participantID, TeamID: 200, ChampionID: 117, ChampionName: "Lulu",
			Role: "UTILITY", Deaths: 1,
		},
		TimelineEvents: []store.ImportedTimelineEvent{{
			SequenceNumber: 10, TimestampMS: deathTimestamp, EventType: "CHAMPION_KILL",
			VictimParticipantID: &participantID,
		}},
	})
	if err != nil {
		t.Fatalf("save imported match: %v", err)
	}
	replayPath := filepath.Join(directory, "NA1-24680.rofl")
	if err := os.WriteFile(replayPath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write replay fixture: %v", err)
	}
	return dataStore, matchID, replayPath
}
