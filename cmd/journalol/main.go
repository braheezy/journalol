package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"journalol/internal/capture"
	"journalol/internal/config"
	"journalol/internal/importer"
	"journalol/internal/leagueconfig"
	"journalol/internal/mcp"
	"journalol/internal/model"
	"journalol/internal/replay"
	"journalol/internal/riot"
	"journalol/internal/store"
	journalweb "journalol/internal/web"
)

var errPrimaryPlayerRequired = errors.New(
	`no primary player is configured; set JOURNALOL_DEMO=true or run "journalol seed-demo" before starting the server`,
)

type demoSeeder interface {
	SeedDemo(context.Context) error
}

type primaryPlayerReader interface {
	PrimaryPlayer(context.Context) (*model.PlayerProfile, error)
}

type demoSeedState uint8

const (
	demoSeedNotRequested demoSeedState = iota
	demoSeedReady
	demoSeedSkippedConflict
)

func main() {
	os.Exit(run())
}

func run() int {
	return runWithIO(os.Args, os.Stdout, os.Stderr)
}

func runWithIO(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	command := "serve"
	if len(args) > 1 {
		command = args[1]
	}

	switch command {
	case "healthcheck":
		if err := healthcheck(cfg.Addr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "serve", "seed-demo", "mcp", "capture":
	default:
		fmt.Fprintf(stderr, "unknown command %q (expected serve, seed-demo, mcp, capture, or healthcheck)\n", command)
		return 2
	}

	logOutput := stdout
	if command == "mcp" {
		// MCP uses stdout as its JSON-RPC transport. Diagnostics must never be
		// mixed into that stream.
		logOutput = stderr
	}
	logger := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	dataStore, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Error("open database", "error", err)
		return 1
	}
	defer func() {
		if err := dataStore.Close(); err != nil {
			logger.Error("close database", "error", err)
		}
	}()

	seedState, err := prepareDemoData(
		context.Background(),
		command,
		cfg.Demo && cfg.Riot == nil,
		dataStore,
	)
	if err != nil {
		logger.Error("seed demo data", "error", err)
		return 1
	}
	if seedState == demoSeedSkippedConflict {
		logger.Warn("automatic demo seed skipped because a non-demo profile exists")
	}
	if command == "seed-demo" {
		logger.Info("demo data is ready")
		return 0
	}
	if command == "capture" {
		if err := requirePrimaryPlayer(context.Background(), dataStore); err != nil {
			logger.Error("cannot run capture", "error", err)
			return 1
		}
		captureContext, stopCapture := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stopCapture()
		if err := runCapture(captureContext, args[2:], dataStore, cfg.DBPath, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		return 0
	}
	if command == "mcp" {
		if err := requirePrimaryPlayer(context.Background(), dataStore); err != nil {
			logger.Error("cannot start MCP server", "error", err)
			return 1
		}
		if err := mcp.NewServer(dataStore, cfg.Location).Serve(context.Background(), os.Stdin, stdout); err != nil {
			logger.Error("serve MCP", "error", err)
			return 1
		}
		return 0
	}

	var riotSync *importer.Service
	startupRiotSync := false
	if cfg.Riot != nil {
		riotClient, err := riot.NewClient(riot.ClientOptions{
			APIKey: cfg.Riot.APIKey,
		})
		if err != nil {
			logger.Error("initialize Riot client", "error", err)
			return 1
		}
		riotSync, err = importer.NewService(
			dataStore,
			riotClient,
			importer.Settings{
				GameName:      cfg.Riot.GameName,
				TagLine:       cfg.Riot.TagLine,
				PlatformRoute: cfg.Riot.PlatformRoute,
				RegionalRoute: cfg.Riot.RegionalRoute,
				HistoryLimit:  cfg.Riot.HistoryLimit,
				PollInterval:  cfg.Riot.PollInterval,
				Location:      cfg.Location,
			},
			logger,
		)
		if err != nil {
			logger.Error("initialize Riot importer", "error", err)
			return 1
		}

		startupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err = riotSync.EnsureProfile(startupContext)
		cancel()
		if err != nil {
			player, playerErr := dataStore.PrimaryPlayer(context.Background())
			switch {
			case errors.Is(err, importer.ErrProfileConflict):
				logger.Error("Riot profile conflicts with this database", "error", err)
				return 1
			case playerErr == nil && !player.IsDemo:
				// An expired development key must not make an existing local
				// journal unavailable. The dashboard exposes a retry control.
				logger.Warn("initial Riot sync failed; serving saved data", "error", err)
			default:
				logger.Error("initial Riot account setup failed", "error", err)
				return 1
			}
		}
		startupRiotSync = cfg.Riot.SyncOnStart
	}

	if err := requirePrimaryPlayer(context.Background(), dataStore); err != nil {
		logger.Error("cannot start server", "error", err)
		return 1
	}

	webOptions := make([]journalweb.Option, 0, 1)
	if riotSync != nil {
		webOptions = append(webOptions, journalweb.WithSyncer(riotSync))
	}
	app, err := journalweb.NewServer(
		dataStore,
		cfg.Location,
		cfg.AllowedHosts,
		logger,
		webOptions...,
	)
	if err != nil {
		logger.Error("initialize web server", "error", err)
		return 1
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info(
			"journalol is ready",
			"address", cfg.Addr,
			"database", cfg.DBPath,
			"riot_configured", riotSync != nil,
		)
		serverErrors <- httpServer.ListenAndServe()
	}()
	if startupRiotSync {
		go func() {
			syncContext, cancel := context.WithTimeout(rootContext, 2*time.Minute)
			defer cancel()
			run, err := riotSync.Sync(syncContext, store.SyncTriggerStartup)
			if err != nil {
				logger.Warn("initial Riot match sync failed; saved data remains available", "error", err)
				return
			}
			if run != nil {
				logger.Info(
					"initial Riot match sync finished",
					"state", run.State,
					"imported", run.ImportedCount,
					"skipped", run.SkippedCount,
					"failed", run.FailedCount,
				)
			}
		}()
	}
	if riotSync != nil && cfg.Riot.PollInterval > 0 {
		go riotSync.RunPolling(rootContext, cfg.Riot.PollInterval)
	}

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve HTTP", "error", err)
			return 1
		}
	case <-rootContext.Done():
		logger.Info("shutting down")
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			logger.Error("graceful shutdown", "error", err)
			return 1
		}
	}

	return 0
}

func runCapture(ctx context.Context, args []string, dataStore *store.Store, databasePath string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: journalol capture <death-clips|restore-config> [options]")
	}
	if args[0] == "restore-config" {
		flags := flag.NewFlagSet("capture restore-config", flag.ContinueOnError)
		flags.SetOutput(stderr)
		leagueRoot := flags.String("league-dir", replay.DefaultLeagueRoot, "League installation directory")
		stateDir := flags.String("state-dir", filepath.Join(filepath.Dir(databasePath), "capture"), "Journalol capture recovery directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected restore-config arguments: %s", strings.Join(flags.Args(), " "))
		}
		configPath := filepath.Join(*leagueRoot, "Config", "game.cfg")
		if err := leagueconfig.RestorePending(configPath, *stateDir); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Restored the League settings saved by the interrupted capture.")
		return nil
	}
	if args[0] != "death-clips" {
		return errors.New("usage: journalol capture <death-clips|restore-config> [options]")
	}
	flags := flag.NewFlagSet("capture death-clips", flag.ContinueOnError)
	flags.SetOutput(stderr)
	matchID := flags.Int64("match", 0, "Journalol match ID")
	replayPath := flags.String("replay", "", "downloaded .rofl replay path (auto-discovered when omitted)")
	before := flags.Duration("before", 60*time.Second, "video before each death")
	after := flags.Duration("after", 10*time.Second, "video after each death")
	outputDir := flags.String("output-dir", filepath.Join(filepath.Dir(databasePath), "clips"), "directory for rendered clips")
	fps := flags.Int("fps", 60, "render frames per second")
	manual := flags.Bool("manual", false, "use a replay already opened by the user")
	leagueRoot := flags.String("league-dir", replay.DefaultLeagueRoot, "League installation directory")
	stateDir := flags.String("state-dir", filepath.Join(filepath.Dir(databasePath), "capture"), "Journalol capture recovery directory")
	windowWidth := flags.Int("window-width", 1280, "temporary League capture window width")
	windowHeight := flags.Int("window-height", 720, "temporary League capture window height")
	startupTimeout := flags.Duration("startup-timeout", 90*time.Second, "maximum time for League to load the replay")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected capture arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *before < 0 || *after <= 0 || *before > 5*time.Minute || *after > 5*time.Minute {
		return errors.New("--before and --after must be no more than 5m, and --after must be positive")
	}
	if *startupTimeout <= 0 || *startupTimeout > 10*time.Minute {
		return errors.New("--startup-timeout must be positive and no more than 10m")
	}
	resolvedReplay, err := captureReplayPath(ctx, dataStore, *matchID, *replayPath)
	if err != nil {
		return err
	}
	client := replay.NewClient()
	service := capture.NewService(dataStore, client)
	clipOptions := capture.DeathClipOptions{
		MatchID: *matchID, ReplayPath: resolvedReplay, OutputDir: *outputDir,
		BeforeMS: before.Milliseconds(), AfterMS: after.Milliseconds(), Codec: "webm", FPS: *fps,
	}
	var clips []model.DeathClip
	if *manual {
		clips, err = service.GenerateDeathClips(ctx, clipOptions)
	} else {
		player, playerErr := dataStore.PrimaryPlayer(ctx)
		if playerErr != nil {
			return fmt.Errorf("load primary player for replay launch: %w", playerErr)
		}
		region, locale, globalsErr := replay.ClientGlobals(*leagueRoot)
		if globalsErr != nil {
			return globalsErr
		}
		automation := capture.NewAutomation(service, replay.NewLauncher())
		clips, err = automation.GenerateDeathClips(ctx, clipOptions, capture.AutomationOptions{
			LeagueRoot:     *leagueRoot,
			ConfigPath:     filepath.Join(*leagueRoot, "Config", "game.cfg"),
			StateDir:       *stateDir,
			PlatformID:     player.PlatformRoute,
			Region:         region,
			Locale:         locale,
			WindowWidth:    *windowWidth,
			WindowHeight:   *windowHeight,
			StartupTimeout: *startupTimeout,
			Progress:       func(message string) { fmt.Fprintln(stdout, message) },
		})
	}
	if err != nil {
		return err
	}
	for _, clip := range clips {
		fmt.Fprintf(stdout, "death %d: %s\n", clip.DeathIndex, clip.OutputPath)
	}
	return nil
}

func captureReplayPath(ctx context.Context, dataStore *store.Store, matchID int64, configuredPath string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath != "" {
		return configuredPath, nil
	}
	if matchID <= 0 {
		return "", errors.New("--match is required")
	}
	detail, err := dataStore.GetMatch(ctx, matchID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("match not found")
	}
	if err != nil {
		return "", fmt.Errorf("load match for replay discovery: %w", err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find downloaded replay directory: %w", err)
	}
	filename := strings.ReplaceAll(detail.RiotMatchID, "_", "-") + ".rofl"
	path := filepath.Join(userHome, "Documents", "League of Legends", "Replays", filename)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("find downloaded replay at %q: %w (download it in the League client or pass --replay)", path, err)
	}
	return path, nil
}

func prepareDemoData(
	ctx context.Context,
	command string,
	demoEnabled bool,
	dataStore demoSeeder,
) (demoSeedState, error) {
	if command != "seed-demo" && !demoEnabled {
		return demoSeedNotRequested, nil
	}

	err := dataStore.SeedDemo(ctx)
	if err == nil {
		return demoSeedReady, nil
	}
	if command == "serve" && errors.Is(err, store.ErrDemoProfileConflict) {
		return demoSeedSkippedConflict, nil
	}
	return demoSeedNotRequested, err
}

func requirePrimaryPlayer(ctx context.Context, dataStore primaryPlayerReader) error {
	_, err := dataStore.PrimaryPlayer(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return errPrimaryPlayerRequired
	default:
		return fmt.Errorf("load primary player: %w", err)
	}
}

func healthcheck(listenAddress string) error {
	url, err := healthURL(listenAddress)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("health request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	return nil
}

func healthURL(listenAddress string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "", fmt.Errorf("parse JOURNALOL_ADDR: %w", err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/readyz", nil
}
