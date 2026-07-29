package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"journalol/internal/model"
	"journalol/internal/store"
)

func TestHealthURLUsesLoopbackForWildcardListener(t *testing.T) {
	got, err := healthURL("0.0.0.0:8080")
	if err != nil {
		t.Fatalf("healthURL() error = %v", err)
	}
	if got != "http://127.0.0.1:8080/readyz" {
		t.Fatalf("healthURL() = %q, want loopback URL", got)
	}
}

func TestRunExplicitSeedConflictReturnsFailure(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "journalol.db")
	saveRealPrimaryPlayer(t, databasePath)
	setRuntimeEnvironment(t, databasePath, false)

	var stdout, stderr bytes.Buffer
	code := runWithIO(
		[]string{"journalol", "seed-demo"},
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("runWithIO(seed-demo) code = %d, want 1", code)
	}
	if strings.Contains(stdout.String(), "demo data is ready") {
		t.Fatalf("conflicting seed claimed success: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), store.ErrDemoProfileConflict.Error()) {
		t.Fatalf("conflicting seed did not explain the failure: %s", stdout.String())
	}
}

func TestRunRefusesMissingPrimaryPlayer(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "journalol.db")
	setRuntimeEnvironment(t, databasePath, false)

	var stdout, stderr bytes.Buffer
	code := runWithIO(
		[]string{"journalol", "serve"},
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("runWithIO(serve) code = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "no primary player is configured") ||
		!strings.Contains(stdout.String(), "JOURNALOL_DEMO=true") {
		t.Fatalf("missing-player startup error was not actionable: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "journalol is ready") {
		t.Fatalf("missing-player startup claimed readiness: %s", stdout.String())
	}
}

func TestDemoPreparationCreatesRequiredPrimaryPlayer(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "journalol.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := dataStore.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	state, err := prepareDemoData(context.Background(), "serve", true, dataStore)
	if err != nil {
		t.Fatalf("prepareDemoData() error = %v", err)
	}
	if state != demoSeedReady {
		t.Fatalf("prepareDemoData() state = %v, want %v", state, demoSeedReady)
	}
	if err := requirePrimaryPlayer(context.Background(), dataStore); err != nil {
		t.Fatalf("requirePrimaryPlayer() after demo seed = %v", err)
	}
}

func TestPrepareDemoData(t *testing.T) {
	t.Parallel()

	seedFailure := errors.New("seed failed")
	tests := []struct {
		name      string
		command   string
		demo      bool
		seedErr   error
		wantState demoSeedState
		wantErr   error
		wantCalls int
	}{
		{
			name:      "explicit seed succeeds",
			command:   "seed-demo",
			seedErr:   nil,
			wantState: demoSeedReady,
			wantCalls: 1,
		},
		{
			name:      "explicit seed rejects real profile conflict",
			command:   "seed-demo",
			seedErr:   store.ErrDemoProfileConflict,
			wantState: demoSeedNotRequested,
			wantErr:   store.ErrDemoProfileConflict,
			wantCalls: 1,
		},
		{
			name:      "automatic seed tolerates real profile conflict",
			command:   "serve",
			demo:      true,
			seedErr:   store.ErrDemoProfileConflict,
			wantState: demoSeedSkippedConflict,
			wantCalls: 1,
		},
		{
			name:      "automatic seed succeeds",
			command:   "serve",
			demo:      true,
			wantState: demoSeedReady,
			wantCalls: 1,
		},
		{
			name:      "disabled automatic seed does nothing",
			command:   "serve",
			demo:      false,
			wantState: demoSeedNotRequested,
			wantCalls: 0,
		},
		{
			name:      "automatic seed propagates unexpected failure",
			command:   "serve",
			demo:      true,
			seedErr:   seedFailure,
			wantState: demoSeedNotRequested,
			wantErr:   seedFailure,
			wantCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dataStore := &fakeRuntimeStore{seedErr: test.seedErr}
			state, err := prepareDemoData(
				context.Background(),
				test.command,
				test.demo,
				dataStore,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("prepareDemoData() error = %v, want %v", err, test.wantErr)
			}
			if state != test.wantState {
				t.Fatalf("prepareDemoData() state = %v, want %v", state, test.wantState)
			}
			if dataStore.seedCalls != test.wantCalls {
				t.Fatalf("SeedDemo() calls = %d, want %d", dataStore.seedCalls, test.wantCalls)
			}
		})
	}
}

func TestRequirePrimaryPlayer(t *testing.T) {
	t.Parallel()

	loadFailure := errors.New("database unavailable")
	tests := []struct {
		name      string
		player    *model.PlayerProfile
		playerErr error
		wantErr   error
	}{
		{
			name:   "primary player is ready",
			player: &model.PlayerProfile{ID: 1},
		},
		{
			name:      "missing player blocks startup clearly",
			playerErr: store.ErrNotFound,
			wantErr:   errPrimaryPlayerRequired,
		},
		{
			name:      "storage failure is preserved",
			playerErr: loadFailure,
			wantErr:   loadFailure,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dataStore := &fakeRuntimeStore{
				player:    test.player,
				playerErr: test.playerErr,
			}
			err := requirePrimaryPlayer(context.Background(), dataStore)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("requirePrimaryPlayer() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

type fakeRuntimeStore struct {
	seedErr   error
	seedCalls int
	player    *model.PlayerProfile
	playerErr error
}

func (s *fakeRuntimeStore) SeedDemo(context.Context) error {
	s.seedCalls++
	return s.seedErr
}

func (s *fakeRuntimeStore) PrimaryPlayer(context.Context) (*model.PlayerProfile, error) {
	return s.player, s.playerErr
}

func saveRealPrimaryPlayer(t *testing.T, databasePath string) {
	t.Helper()

	dataStore, err := store.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_, saveErr := dataStore.SavePrimaryPlayer(context.Background(), model.PlayerProfile{
		GameName:      "RealPlayer",
		TagLine:       "LOCAL",
		PlatformRoute: "NA1",
		RegionalRoute: "AMERICAS",
		PUUID:         "real-player-puuid",
	})
	closeErr := dataStore.Close()
	if saveErr != nil {
		t.Fatalf("save real primary player: %v", saveErr)
	}
	if closeErr != nil {
		t.Fatalf("close store: %v", closeErr)
	}
}

func setRuntimeEnvironment(t *testing.T, databasePath string, demo bool) {
	t.Helper()

	t.Setenv("JOURNALOL_ADDR", "invalid-listen-address")
	t.Setenv("JOURNALOL_DB_PATH", databasePath)
	t.Setenv("JOURNALOL_TIMEZONE", "UTC")
	t.Setenv("JOURNALOL_ALLOWED_HOSTS", "")
	if demo {
		t.Setenv("JOURNALOL_DEMO", "true")
	} else {
		t.Setenv("JOURNALOL_DEMO", "false")
	}
}
