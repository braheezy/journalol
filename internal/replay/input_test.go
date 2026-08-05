package replay

import (
	"errors"
	"strings"
	"testing"
)

func TestPostKeyWith(t *testing.T) {
	var gotPID int
	var gotKey uint16
	err := postKeyWith(4217, SelectSelfKey, func(pid int, virtualKey uint16) error {
		gotPID = pid
		gotKey = virtualKey
		return nil
	})
	if err != nil {
		t.Fatalf("postKeyWith() error = %v", err)
	}
	if gotPID != 4217 {
		t.Fatalf("poster PID = %d, want 4217", gotPID)
	}
	if gotKey != 0x7a {
		t.Fatalf("poster key = %#x, want 0x7a", gotKey)
	}
}

func TestPostKeyWithRejectsInvalidPID(t *testing.T) {
	called := false
	err := postKeyWith(0, SelectSelfKey, func(_ int, _ uint16) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("postKeyWith() error = %v, want positive-PID error", err)
	}
	if called {
		t.Fatal("poster called for invalid PID")
	}
}

func TestPostKeyWithRejectsMissingPoster(t *testing.T) {
	err := postKeyWith(4217, SelectSelfKey, nil)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("postKeyWith() error = %v, want missing-poster error", err)
	}
}

func TestPostKeyWithWrapsPlatformError(t *testing.T) {
	want := errors.New("permission denied")
	err := postKeyWith(4217, 0x0c, func(_ int, _ uint16) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("postKeyWith() error = %v, want wrapped platform error", err)
	}
	if !strings.Contains(err.Error(), "0x000c") || !strings.Contains(err.Error(), "4217") {
		t.Fatalf("postKeyWith() error = %q, want key and PID context", err)
	}
}

func TestPostKeyRejectsInvalidPIDWithoutUsingPlatform(t *testing.T) {
	err := PostKey(-1, SelectSelfKey)
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("PostKey() error = %v, want positive-PID error", err)
	}
}
