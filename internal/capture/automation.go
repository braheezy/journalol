package capture

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"journalol/internal/leagueconfig"
	"journalol/internal/model"
	"journalol/internal/replay"
)

// AutomationOptions contain host-only settings for one managed replay. None
// of these paths or process controls are exposed through the web app or MCP.
type AutomationOptions struct {
	LeagueRoot     string
	ConfigPath     string
	StateDir       string
	PlatformID     string
	Region         string
	Locale         string
	WindowWidth    int
	WindowHeight   int
	StartupTimeout time.Duration
	Progress       func(string)
}

type replayLauncher interface {
	EnsureParticipantInput() error
	Launch(context.Context, replay.LaunchOptions) (int, error)
	VerifyOwned(context.Context, int, string) error
	SelectParticipant(context.Context, int, string, uint16) error
	StopOwned(context.Context, int, string) error
	FindSelectedReplay(context.Context, string) (int, error)
}

type configLease interface {
	Restore() error
}

type configApplier func(leagueconfig.CaptureSettings) (configLease, error)

// Automation adds the reversible host lifecycle around the platform-neutral
// death clip renderer.
type Automation struct {
	service     *Service
	launcher    replayLauncher
	applyConfig configApplier
}

func NewAutomation(service *Service, launcher *replay.Launcher) *Automation {
	return &Automation{
		service:  service,
		launcher: launcher,
		applyConfig: func(settings leagueconfig.CaptureSettings) (configLease, error) {
			return leagueconfig.Apply(settings)
		},
	}
}

// GenerateDeathClips validates first, temporarily applies capture settings,
// launches the selected replay, verifies its process, renders, then restores
// the original settings after the owned game exits.
func (a *Automation) GenerateDeathClips(
	ctx context.Context,
	clipOptions DeathClipOptions,
	automationOptions AutomationOptions,
) (clips []model.DeathClip, resultErr error) {
	if a == nil || a.service == nil || a.service.replay == nil || a.launcher == nil || a.applyConfig == nil {
		return nil, errors.New("automated capture is not configured")
	}
	automationOptions.LeagueRoot = strings.TrimSpace(automationOptions.LeagueRoot)
	automationOptions.ConfigPath = strings.TrimSpace(automationOptions.ConfigPath)
	automationOptions.StateDir = strings.TrimSpace(automationOptions.StateDir)
	automationOptions.PlatformID = strings.ToUpper(strings.TrimSpace(automationOptions.PlatformID))
	automationOptions.Region = strings.ToUpper(strings.TrimSpace(automationOptions.Region))
	automationOptions.Locale = strings.TrimSpace(automationOptions.Locale)
	if automationOptions.LeagueRoot == "" || automationOptions.ConfigPath == "" || automationOptions.StateDir == "" ||
		automationOptions.PlatformID == "" || automationOptions.Region == "" || automationOptions.Locale == "" {
		return nil, errors.New("League installation, config, capture state, platform, region, and locale are required")
	}
	if automationOptions.StartupTimeout <= 0 {
		return nil, errors.New("replay startup timeout must be positive")
	}

	plan, clipOptions, err := a.service.prepare(ctx, clipOptions)
	if err != nil {
		return nil, err
	}
	focusKey, focusKeyLabel, err := replay.SpectatorFocusKey(plan.subject.ParticipantID, plan.subject.TeamID)
	if err != nil {
		return nil, fmt.Errorf("derive replay focus key: %w", err)
	}
	if err := a.launcher.EnsureParticipantInput(); err != nil {
		return nil, fmt.Errorf("prepare replay participant focus: %w", err)
	}
	notifyProgress(automationOptions.Progress, "Validated the replay and %d death event(s).", len(plan.events))

	lease, err := a.applyConfig(leagueconfig.CaptureSettings{
		ConfigPath: automationOptions.ConfigPath,
		StateDir:   automationOptions.StateDir,
		Width:      automationOptions.WindowWidth,
		Height:     automationOptions.WindowHeight,
	})
	if err != nil {
		return nil, err
	}
	launched := false
	ownedPID := 0
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var cleanupErr error
		if launched {
			if ownedPID == 0 {
				if discoveredPID, findErr := a.launcher.FindSelectedReplay(cleanupContext, clipOptions.ReplayPath); findErr == nil {
					ownedPID = discoveredPID
				}
			}
			if ownedPID > 0 {
				notifyProgress(automationOptions.Progress, "Closing the capture replay.")
				cleanupErr = a.launcher.StopOwned(cleanupContext, ownedPID, clipOptions.ReplayPath)
			}
		}
		restoreErr := lease.Restore()
		if restoreErr == nil {
			notifyProgress(automationOptions.Progress, "Restored the original League video settings.")
		}
		resultErr = errors.Join(resultErr, cleanupErr, restoreErr)
	}()

	resolvedLeagueRoot, err := filepath.Abs(automationOptions.LeagueRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve League installation: %w", err)
	}
	notifyProgress(automationOptions.Progress, "Launching the replay in a %dx%d window.", automationOptions.WindowWidth, automationOptions.WindowHeight)
	launchedPID, err := a.launcher.Launch(ctx, replay.LaunchOptions{
		LeagueRoot: resolvedLeagueRoot,
		ReplayPath: clipOptions.ReplayPath,
		PlatformID: automationOptions.PlatformID,
		Region:     automationOptions.Region,
		Locale:     automationOptions.Locale,
	})
	if err != nil {
		return nil, err
	}
	launched = true
	ownedPID = launchedPID
	notifyProgress(automationOptions.Progress, "Waiting for League to load the replay API.")
	game, playback, err := a.service.replay.WaitReady(ctx, ownedPID, automationOptions.StartupTimeout)
	if err != nil {
		return nil, err
	}
	ownedPID = game.ProcessID
	if err := a.launcher.VerifyOwned(ctx, ownedPID, clipOptions.ReplayPath); err != nil {
		return nil, fmt.Errorf("verify launched replay: %w", err)
	}
	notifyProgress(
		automationOptions.Progress,
		"Replay ready; locking the camera to %s (participant %d, spectator key %s).",
		plan.subject.Champion,
		plan.subject.ParticipantID,
		focusKeyLabel,
	)
	prepareCamera := func(cameraContext context.Context) error {
		return a.service.replay.PreparePlayerCamera(cameraContext, func() error {
			return a.launcher.SelectParticipant(cameraContext, ownedPID, clipOptions.ReplayPath, focusKey)
		})
	}
	notifyProgress(automationOptions.Progress, "Rendering %d death clip(s).", len(plan.events))
	return a.service.render(ctx, plan, clipOptions, playback, prepareCamera)
}

func notifyProgress(destination func(string), format string, args ...any) {
	if destination != nil {
		destination(fmt.Sprintf(format, args...))
	}
}
