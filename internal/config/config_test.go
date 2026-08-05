package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	clearEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Addr != defaultAddr {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, defaultAddr)
	}
	if cfg.DBPath != defaultDBPath {
		t.Fatalf("DBPath = %q, want %q", cfg.DBPath, defaultDBPath)
	}
	if !cfg.Demo {
		t.Fatal("Demo = false, want true")
	}
	if cfg.Location.String() != "UTC" {
		t.Fatalf("Location = %q, want UTC", cfg.Location)
	}
	if cfg.Riot != nil {
		t.Fatalf("Riot = %#v, want nil", cfg.Riot)
	}
}

func TestLoadRejectsInvalidDemoFlag(t *testing.T) {
	t.Setenv("JOURNALOL_DEMO", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid boolean error")
	}
}

func TestLoadRiotConfigDerivesRegionalRoute(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("RIOT_API_KEY", "RGAPI-test-secret")
	t.Setenv("JOURNALOL_RIOT_GAME_NAME", "Coach Cat")
	t.Setenv("JOURNALOL_RIOT_TAG_LINE", "NA1")
	t.Setenv("JOURNALOL_RIOT_PLATFORM_ROUTE", "na1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Riot == nil {
		t.Fatal("Riot = nil, want configuration")
	}
	if cfg.Riot.APIKey != "RGAPI-test-secret" ||
		cfg.Riot.PlatformRoute != "NA1" ||
		cfg.Riot.RegionalRoute != "AMERICAS" {
		t.Fatalf("Riot = %#v", cfg.Riot)
	}
	if cfg.Riot.HistoryLimit != 20 || cfg.Riot.PollInterval != 5*time.Minute ||
		!cfg.Riot.SyncOnStart {
		t.Fatalf("Riot defaults = %#v", cfg.Riot)
	}
}

func TestLoadRiotKeyFileAndOverrides(t *testing.T) {
	clearEnvironment(t)
	keyPath := filepath.Join(t.TempDir(), "riot-key")
	if err := os.WriteFile(keyPath, []byte("  RGAPI-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIOT_API_KEY_FILE", keyPath)
	t.Setenv("JOURNALOL_RIOT_GAME_NAME", "Coach Cat")
	t.Setenv("JOURNALOL_RIOT_TAG_LINE", "EUW")
	t.Setenv("JOURNALOL_RIOT_PLATFORM_ROUTE", "EUW1")
	t.Setenv("JOURNALOL_RIOT_REGIONAL_ROUTE", "EUROPE")
	t.Setenv("JOURNALOL_RIOT_HISTORY_LIMIT", "42")
	t.Setenv("JOURNALOL_RIOT_POLL_INTERVAL", "15m")
	t.Setenv("JOURNALOL_RIOT_SYNC_ON_START", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Riot.APIKey != "RGAPI-from-file" ||
		cfg.Riot.HistoryLimit != 42 ||
		cfg.Riot.PollInterval != 15*time.Minute ||
		cfg.Riot.SyncOnStart {
		t.Fatalf("Riot = %#v", cfg.Riot)
	}
}

func TestLoadRejectsPartialAndInvalidRiotConfig(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantErr string
	}{
		{
			name:    "partial",
			values:  map[string]string{"RIOT_API_KEY": "secret"},
			wantErr: "incomplete Riot configuration",
		},
		{
			name: "unknown platform",
			values: map[string]string{
				"RIOT_API_KEY":                  "secret",
				"JOURNALOL_RIOT_GAME_NAME":      "Player",
				"JOURNALOL_RIOT_TAG_LINE":       "TAG",
				"JOURNALOL_RIOT_PLATFORM_ROUTE": "MARS1",
			},
			wantErr: "unsupported",
		},
		{
			name: "too-fast polling",
			values: map[string]string{
				"RIOT_API_KEY":                  "secret",
				"JOURNALOL_RIOT_GAME_NAME":      "Player",
				"JOURNALOL_RIOT_TAG_LINE":       "TAG",
				"JOURNALOL_RIOT_PLATFORM_ROUTE": "NA1",
				"JOURNALOL_RIOT_POLL_INTERVAL":  "30s",
			},
			wantErr: "at least 1m",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEnvironment(t)
			for key, value := range test.values {
				t.Setenv(key, value)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"JOURNALOL_ADDR",
		"JOURNALOL_DB_PATH",
		"JOURNALOL_TIMEZONE",
		"JOURNALOL_DEMO",
		"JOURNALOL_ALLOWED_HOSTS",
		"RIOT_API_KEY",
		"RIOT_API_KEY_FILE",
		"JOURNALOL_RIOT_GAME_NAME",
		"JOURNALOL_RIOT_TAG_LINE",
		"JOURNALOL_RIOT_PLATFORM_ROUTE",
		"JOURNALOL_RIOT_REGIONAL_ROUTE",
		"JOURNALOL_RIOT_HISTORY_LIMIT",
		"JOURNALOL_RIOT_POLL_INTERVAL",
		"JOURNALOL_RIOT_SYNC_ON_START",
	} {
		t.Setenv(key, "")
	}
}
