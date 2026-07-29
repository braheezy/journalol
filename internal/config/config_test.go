package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("JOURNALOL_ADDR", "")
	t.Setenv("JOURNALOL_DB_PATH", "")
	t.Setenv("JOURNALOL_TIMEZONE", "")
	t.Setenv("JOURNALOL_DEMO", "")
	t.Setenv("JOURNALOL_ALLOWED_HOSTS", "")

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
}

func TestLoadRejectsInvalidDemoFlag(t *testing.T) {
	t.Setenv("JOURNALOL_DEMO", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid boolean error")
	}
}
