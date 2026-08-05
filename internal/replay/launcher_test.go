package replay

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLaunchArgumentsKeepReplayPathAsOneArgument(t *testing.T) {
	t.Parallel()
	options := LaunchOptions{
		LeagueRoot: "/Applications/League of Legends.app/Contents/LoL",
		ReplayPath: "/Users/player/Documents/League of Legends/Replays/NA1-123.rofl",
		PlatformID: "NA1",
		Region:     "NA",
		Locale:     "en_US",
	}
	want := []string{
		options.ReplayPath,
		"-GameBaseDir=" + options.LeagueRoot,
		"-Region=NA",
		"-PlatformID=NA1",
		"-Locale=en_US",
		"-SkipBuild",
		"-EnableCrashpad=true",
		"-UseMetal=1:1",
	}
	if got := launchArguments(options); !reflect.DeepEqual(got, want) {
		t.Fatalf("launchArguments() = %#v, want %#v", got, want)
	}
}

func TestParseGameProcessesFiltersLauncherAndOtherCommands(t *testing.T) {
	t.Parallel()
	output := `
  101 /Applications/League of Legends.app/Contents/LoL/LeagueClient.app/Contents/MacOS/LeagueClient
  202 /Applications/League of Legends.app/Contents/LoL/Game/LeagueofLegends.app/Contents/MacOS/LeagueofLegends /Users/me/a.rofl -Region=NA
  303 /usr/bin/other
`
	processes := parseGameProcesses(output)
	if len(processes) != 1 || processes[0].PID != 202 || !strings.Contains(processes[0].Command, "/Users/me/a.rofl") {
		t.Fatalf("parseGameProcesses() = %#v", processes)
	}
}

func TestVerifyOwnedRequiresSelectedReplayInProcessArguments(t *testing.T) {
	t.Parallel()
	launcher := NewLauncher()
	launcher.owned[42] = ownedProcess{replayPath: "/tmp/NA1-123.rofl"}
	var inspections atomic.Int32
	launcher.commandOutput = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		command := "42 /Applications/League of Legends.app/Contents/LoL/Game/LeagueofLegends.app/Contents/MacOS/LeagueofLegends"
		if inspections.Add(1) == 1 {
			command += " /tmp/NA1-123.rofl -Region=NA"
		}
		return []byte(command + "\n"), nil
	}
	if err := launcher.VerifyOwned(context.Background(), 42, "/tmp/NA1-123.rofl"); err != nil {
		t.Fatalf("VerifyOwned(): %v", err)
	}
	if err := launcher.VerifyOwned(context.Background(), 42, "/tmp/NA1-123.rofl"); err != nil {
		t.Fatalf("VerifyOwned() after League rewrote its process title: %v", err)
	}
	if err := launcher.VerifyOwned(context.Background(), 42, "/tmp/NA1-999.rofl"); err == nil {
		t.Fatal("VerifyOwned() accepted a different replay")
	}
}

func TestSelectParticipantVerifiesOwnershipBeforePostingKey(t *testing.T) {
	t.Parallel()
	launcher := NewLauncher()
	launcher.owned[42] = ownedProcess{replayPath: "/tmp/NA1-123.rofl"}
	launcher.commandOutput = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("42 /Applications/League of Legends.app/Contents/LoL/Game/LeagueofLegends.app/Contents/MacOS/LeagueofLegends /tmp/NA1-123.rofl\n"), nil
	}
	var gotPID int
	keys := make([]uint16, 0, 2)
	launcher.postKey = func(pid int, virtualKey uint16) error {
		gotPID = pid
		keys = append(keys, virtualKey)
		return nil
	}
	if err := launcher.SelectParticipant(context.Background(), 42, "/tmp/NA1-123.rofl", 17); err != nil {
		t.Fatalf("SelectParticipant(): %v", err)
	}
	if gotPID != 42 {
		t.Fatalf("posted keys to pid=%d, want pid=42", gotPID)
	}
	if !reflect.DeepEqual(keys, []uint16{17, 17}) {
		t.Fatalf("spectator keys = %#v, want participant double press", keys)
	}
}

func TestClientGlobalsReadsInstalledRegionAndLocale(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configDirectory := filepath.Join(root, "Config")
	if err := os.MkdirAll(configDirectory, 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	settings := "install:\n    globals:\n        locale: \"en_GB\"\n        region: \"EUW\"\n    lcu-settings: {}\n"
	if err := os.WriteFile(filepath.Join(configDirectory, "LeagueClientSettings.yaml"), []byte(settings), 0o600); err != nil {
		t.Fatalf("write client settings: %v", err)
	}
	region, locale, err := ClientGlobals(root)
	if err != nil {
		t.Fatalf("ClientGlobals(): %v", err)
	}
	if region != "EUW" || locale != "en_GB" {
		t.Fatalf("ClientGlobals() = %q, %q", region, locale)
	}
}
