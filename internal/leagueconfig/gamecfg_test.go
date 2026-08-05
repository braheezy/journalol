package leagueconfig

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyAndRestorePreservesOriginalConfig(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "game.cfg")
	stateDir := filepath.Join(directory, "capture-state")
	original := []byte("[General]\r\nWidth=3024\r\nWindowMode=0\r\nKeepThis=yes\r\n\r\n[HUD]\r\nScale=1\r\n")
	if err := os.WriteFile(configPath, original, 0o640); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}

	lease, err := Apply(CaptureSettings{
		ConfigPath: configPath,
		StateDir:   stateDir,
		Width:      1280,
		Height:     720,
	})
	if err != nil {
		t.Fatalf("Apply(): %v", err)
	}
	patched, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read patched config: %v", err)
	}
	for _, expected := range []string{"EnableReplayApi=1", "WindowMode=1", "Width=1280", "Height=720", "EnableDirectedCamera=0", "KeepThis=yes", "[HUD]"} {
		if !bytes.Contains(patched, []byte(expected)) {
			t.Fatalf("patched config does not contain %q:\n%s", expected, patched)
		}
	}
	if bytes.Contains(bytes.ReplaceAll(patched, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("patch introduced a non-CRLF line ending: %q", patched)
	}
	if _, err := Apply(CaptureSettings{ConfigPath: configPath, StateDir: stateDir, Width: 1280, Height: 720}); !errors.Is(err, ErrCaptureBusy) {
		t.Fatalf("second Apply() error = %v, want ErrCaptureBusy", err)
	}

	if err := lease.Restore(); err != nil {
		t.Fatalf("Restore(): %v", err)
	}
	if err := lease.Restore(); err != nil {
		t.Fatalf("second Restore(): %v", err)
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored config differs\ngot:  %q\nwant: %q", restored, original)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat restored config: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode = %o, want 640", info.Mode().Perm())
	}
	for _, name := range []string{backupFilename, manifestFilename} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restored state file %s still exists: %v", name, err)
		}
	}
}

func TestRestorePendingRecoversInterruptedCapture(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "game.cfg")
	stateDir := filepath.Join(directory, "capture-state")
	original := []byte("[General]\nWindowMode=0\nWidth=1920\nHeight=1080\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	lease, err := Apply(CaptureSettings{ConfigPath: configPath, StateDir: stateDir, Width: 1024, Height: 768})
	if err != nil {
		t.Fatalf("Apply(): %v", err)
	}
	// Simulate process death: the OS releases its advisory lock, but deferred
	// restoration never runs and the durable backup remains.
	lease.releaseLock()
	if err := RestorePending(configPath, stateDir); err != nil {
		t.Fatalf("RestorePending(): %v", err)
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored config = %q, want %q", restored, original)
	}
}

func TestApplyRejectsDuplicateCaptureKeysWithoutChangingFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "game.cfg")
	original := []byte("[General]\nWidth=800\nwidth=900\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	_, err := Apply(CaptureSettings{ConfigPath: configPath, StateDir: filepath.Join(directory, "state"), Width: 1280, Height: 720})
	if err == nil || !strings.Contains(err.Error(), "duplicate Width") {
		t.Fatalf("Apply() error = %v, want duplicate Width error", err)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read config after rejected patch: %v", readErr)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("rejected patch changed config: %q", after)
	}
}

func TestPatchGeneralAddsMissingSection(t *testing.T) {
	t.Parallel()
	patched, err := patchGeneral([]byte("[HUD]\nScale=1"), map[string]string{
		"EnableReplayApi": "1", "WindowMode": "1", "Width": "1280", "Height": "720",
	})
	if err != nil {
		t.Fatalf("patchGeneral(): %v", err)
	}
	wantSuffix := "\n\n[General]\nEnableReplayApi=1\nWindowMode=1\nWidth=1280\nHeight=720\n"
	if !strings.HasSuffix(string(patched), wantSuffix) {
		t.Fatalf("patched config = %q, want suffix %q", patched, wantSuffix)
	}
}
