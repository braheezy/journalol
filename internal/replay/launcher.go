package replay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const DefaultLeagueRoot = "/Applications/League of Legends.app/Contents/LoL"

// LaunchOptions describe the installed client and selected downloaded replay.
type LaunchOptions struct {
	LeagueRoot string
	ReplayPath string
	PlatformID string
	Region     string
	Locale     string
}

type processInfo struct {
	PID     int
	Command string
}

type ownedProcess struct {
	replayPath string
	verified   bool
}

// Launcher starts only the nested League game bundle. The normal League
// launcher is deliberately left alone so capture cannot affect its session.
type Launcher struct {
	startCommand  func(string, string, ...string) (int, error)
	commandOutput func(context.Context, string, ...string) ([]byte, error)
	ensureInput   func() error
	postKey       virtualKeyPoster
	pollInterval  time.Duration
	ownedMu       sync.Mutex
	owned         map[int]ownedProcess
}

func NewLauncher() *Launcher {
	return &Launcher{
		startCommand: func(directory, name string, args ...string) (int, error) {
			command := exec.Command(name, args...)
			command.Dir = directory
			command.Stdout = io.Discard
			command.Stderr = io.Discard
			if err := command.Start(); err != nil {
				return 0, err
			}
			pid := command.Process.Pid
			go func() { _ = command.Wait() }()
			return pid, nil
		},
		commandOutput: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		ensureInput:  EnsureInputAccess,
		postKey:      PostKey,
		pollInterval: 250 * time.Millisecond,
		owned:        make(map[int]ownedProcess),
	}
}

// EnsureParticipantInput checks the host capability required to select a
// replay participant before Journalol changes settings or launches League.
func (l *Launcher) EnsureParticipantInput() error {
	if l.ensureInput == nil {
		return errors.New("replay participant input check is not configured")
	}
	return l.ensureInput()
}

// SelectParticipant double-presses one spectator participant key only after
// re-verifying the exact replay process Journalol launched. League's replay
// spectator uses the first press to select/center and the second to follow.
// This avoids depending on the player's normal or WASD camera-lock binding.
// Posting directly to the PID avoids activating League or moving the user to
// its assigned macOS Space.
func (l *Launcher) SelectParticipant(ctx context.Context, pid int, replayPath string, virtualKey uint16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.VerifyOwned(ctx, pid, replayPath); err != nil {
		return fmt.Errorf("verify replay before selecting participant: %w", err)
	}
	if l.postKey == nil {
		return errors.New("replay participant key sender is not configured")
	}
	// Replay API cameraAttached is a 3D render-camera feature, not the native
	// top-down spectator follow state. Use League's own double participant input
	// instead of trying to emulate that state over HTTP.
	for press := 0; press < 2; press++ {
		if err := l.postKey(pid, virtualKey); err != nil {
			return fmt.Errorf("send spectator camera key: %w", err)
		}
		if press == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(125 * time.Millisecond):
			}
		}
	}
	return nil
}

// Launch starts the nested game executable from League's required Game
// working directory. macOS still associates the process with its enclosing
// app bundle and bundle ID, which is what the user's Space assignment targets.
func (l *Launcher) Launch(ctx context.Context, options LaunchOptions) (int, error) {
	resolved, err := validateLaunchOptions(options)
	if err != nil {
		return 0, err
	}
	if runtime.GOOS != "darwin" {
		return 0, errors.New("automatic replay launch is currently supported only on macOS; use --manual on this platform")
	}
	running, err := l.gameProcesses(ctx)
	if err != nil {
		return 0, fmt.Errorf("check for a running League game: %w", err)
	}
	if len(running) > 0 {
		return 0, fmt.Errorf("League game process %d is already running; close the game or use --manual with the intended replay", running[0].PID)
	}
	gameDirectory := filepath.Join(resolved.LeagueRoot, "Game")
	executable := filepath.Join(gameDirectory, "LeagueofLegends.app", "Contents", "MacOS", "LeagueofLegends")
	arguments := launchArguments(resolved)
	pid, err := l.startCommand(gameDirectory, executable, arguments...)
	if err != nil {
		return 0, fmt.Errorf("launch League replay: %w", err)
	}
	l.ownedMu.Lock()
	l.owned[pid] = ownedProcess{replayPath: resolved.ReplayPath}
	l.ownedMu.Unlock()
	return pid, nil
}

// VerifyOwned confirms that the process exposed by port 2999 is the game
// launched with the selected replay before Journalol records or terminates it.
func (l *Launcher) VerifyOwned(ctx context.Context, pid int, replayPath string) error {
	if pid <= 0 {
		return errors.New("League replay process ID must be positive")
	}
	resolvedReplay, err := filepath.Abs(strings.TrimSpace(replayPath))
	if err != nil {
		return fmt.Errorf("resolve replay path: %w", err)
	}
	l.ownedMu.Lock()
	owned, known := l.owned[pid]
	l.ownedMu.Unlock()
	if !known || owned.replayPath != resolvedReplay {
		return fmt.Errorf("process %d was not launched by Journalol for replay %q", pid, resolvedReplay)
	}
	process, err := l.process(ctx, pid)
	if err != nil {
		return err
	}
	if !isLeagueGameCommand(process.Command) || (!owned.verified && !strings.Contains(process.Command, resolvedReplay)) {
		return fmt.Errorf("process %d does not belong to the selected replay %q", pid, resolvedReplay)
	}
	if !owned.verified {
		l.ownedMu.Lock()
		owned.verified = true
		l.owned[pid] = owned
		l.ownedMu.Unlock()
	}
	return nil
}

// StopOwned gracefully terminates the verified replay, escalating to a force
// kill only if that exact process ignores SIGTERM. It never targets the League
// launcher or an unverified game.
func (l *Launcher) StopOwned(ctx context.Context, pid int, replayPath string) error {
	if err := l.VerifyOwned(ctx, pid, replayPath); err != nil {
		if isProcessGone(err) {
			l.forget(pid)
			return nil
		}
		return err
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find launched League replay process: %w", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop launched League replay process: %w", err)
	}
	if l.waitForExit(ctx, pid, 15*time.Second) {
		l.forget(pid)
		return nil
	}
	// Verify again to protect against the extremely unlikely possibility of PID
	// reuse during the graceful-shutdown window.
	if err := l.VerifyOwned(ctx, pid, replayPath); err != nil {
		if isProcessGone(err) {
			l.forget(pid)
			return nil
		}
		return err
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("force-stop launched League replay process: %w", err)
	}
	if !l.waitForExit(ctx, pid, 5*time.Second) {
		return fmt.Errorf("League replay process %d did not exit", pid)
	}
	l.forget(pid)
	return nil
}

func (l *Launcher) forget(pid int) {
	l.ownedMu.Lock()
	delete(l.owned, pid)
	l.ownedMu.Unlock()
}

// FindSelectedReplay locates a process for cleanup if startup fails after the
// app bundle has opened but before the Replay API becomes ready.
func (l *Launcher) FindSelectedReplay(ctx context.Context, replayPath string) (int, error) {
	resolvedReplay, err := filepath.Abs(strings.TrimSpace(replayPath))
	if err != nil {
		return 0, fmt.Errorf("resolve replay path: %w", err)
	}
	processes, err := l.gameProcesses(ctx)
	if err != nil {
		return 0, err
	}
	for _, process := range processes {
		if strings.Contains(process.Command, resolvedReplay) {
			return process.PID, nil
		}
	}
	return 0, errors.New("selected League replay process was not found")
}

// ClientGlobals reads the two launch values the installed League client itself
// uses, avoiding hard-coded region or language assumptions.
func ClientGlobals(leagueRoot string) (region string, locale string, err error) {
	path := filepath.Join(strings.TrimSpace(leagueRoot), "Config", "LeagueClientSettings.yaml")
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("read League client settings: %w", err)
	}
	defer file.Close()
	inGlobals := false
	globalsIndent := -1
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if trimmed == "globals:" {
			inGlobals = true
			globalsIndent = indent
			continue
		}
		if inGlobals && indent <= globalsIndent {
			break
		}
		if !inGlobals {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch strings.TrimSpace(key) {
		case "region":
			region = strings.ToUpper(value)
		case "locale":
			locale = value
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("scan League client settings: %w", err)
	}
	if region == "" || locale == "" {
		return "", "", errors.New("League client settings do not contain install.globals region and locale")
	}
	return region, locale, nil
}

func validateLaunchOptions(options LaunchOptions) (LaunchOptions, error) {
	var err error
	leagueRoot := strings.TrimSpace(options.LeagueRoot)
	if leagueRoot == "" {
		return LaunchOptions{}, errors.New("League installation directory is required")
	}
	options.LeagueRoot, err = filepath.Abs(leagueRoot)
	if err != nil {
		return LaunchOptions{}, fmt.Errorf("resolve League installation directory: %w", err)
	}
	replayPath := strings.TrimSpace(options.ReplayPath)
	if replayPath == "" {
		return LaunchOptions{}, errors.New("replay path is required")
	}
	options.ReplayPath, err = filepath.Abs(replayPath)
	if err != nil {
		return LaunchOptions{}, fmt.Errorf("resolve replay path: %w", err)
	}
	options.PlatformID = strings.ToUpper(strings.TrimSpace(options.PlatformID))
	options.Region = strings.ToUpper(strings.TrimSpace(options.Region))
	options.Locale = strings.TrimSpace(options.Locale)
	if options.PlatformID == "" || options.Region == "" || options.Locale == "" {
		return LaunchOptions{}, errors.New("League platform, region, and locale are required")
	}
	replayInfo, err := os.Stat(options.ReplayPath)
	if err != nil {
		return LaunchOptions{}, fmt.Errorf("read replay file: %w", err)
	}
	if replayInfo.IsDir() || !strings.EqualFold(filepath.Ext(options.ReplayPath), ".rofl") {
		return LaunchOptions{}, errors.New("replay path must be a .rofl file")
	}
	bundlePath := filepath.Join(options.LeagueRoot, "Game", "LeagueofLegends.app")
	if info, err := os.Stat(bundlePath); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("path is not an application bundle")
		}
		return LaunchOptions{}, fmt.Errorf("find League game bundle at %q: %w", bundlePath, err)
	}
	return options, nil
}

func launchArguments(options LaunchOptions) []string {
	return []string{
		options.ReplayPath,
		"-GameBaseDir=" + options.LeagueRoot,
		"-Region=" + options.Region,
		"-PlatformID=" + options.PlatformID,
		"-Locale=" + options.Locale,
		"-SkipBuild",
		"-EnableCrashpad=true",
		"-UseMetal=1:1",
	}
}

func (l *Launcher) gameProcesses(ctx context.Context) ([]processInfo, error) {
	output, err := l.commandOutput(ctx, "/bin/ps", "-axo", "pid=,args=")
	if err != nil {
		return nil, err
	}
	return parseGameProcesses(string(output)), nil
}

func (l *Launcher) process(ctx context.Context, pid int) (processInfo, error) {
	output, err := l.commandOutput(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "pid=,args=")
	if err != nil {
		return processInfo{}, fmt.Errorf("League replay process %d is no longer running", pid)
	}
	processes := parseProcessLines(string(output))
	if len(processes) != 1 || processes[0].PID != pid {
		return processInfo{}, fmt.Errorf("League replay process %d is no longer running", pid)
	}
	return processes[0], nil
}

func parseGameProcesses(output string) []processInfo {
	all := parseProcessLines(output)
	result := make([]processInfo, 0)
	for _, process := range all {
		if isLeagueGameCommand(process.Command) {
			result = append(result, process)
		}
	}
	return result
}

func parseProcessLines(output string) []processInfo {
	result := make([]processInfo, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidText, command, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if err != nil || pid <= 0 {
			continue
		}
		result = append(result, processInfo{PID: pid, Command: strings.TrimSpace(command)})
	}
	return result
}

func isLeagueGameCommand(command string) bool {
	return strings.Contains(command, "/LeagueofLegends.app/Contents/MacOS/LeagueofLegends")
}

func (l *Launcher) waitForExit(ctx context.Context, pid int, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(l.pollInterval)
	defer ticker.Stop()
	for {
		_, err := l.process(ctx, pid)
		if err != nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func isProcessGone(err error) bool {
	return err != nil && strings.Contains(err.Error(), "is no longer running")
}
