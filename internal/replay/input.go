package replay

import (
	"errors"
	"fmt"
)

// SelectSelfKey is the macOS virtual key code for F1, League's default
// "Select Self" spectator shortcut.
const SelectSelfKey uint16 = 0x7a

type virtualKeyPoster func(pid int, virtualKey uint16) error

// EnsureInputAccess checks the host permission needed for PID-targeted replay
// selection. Calling it before League launch keeps a missing one-time macOS
// permission from opening and then immediately closing a replay.
func EnsureInputAccess() error {
	return platformEnsureInputAccess()
}

// PostKey sends one key press directly to an already-verified League replay
// process. Process ownership is deliberately checked by Launcher before this
// lower-level function is called.
func PostKey(pid int, virtualKey uint16) error {
	return postKeyWith(pid, virtualKey, platformPostKey)
}

func postKeyWith(pid int, virtualKey uint16, post virtualKeyPoster) error {
	if pid <= 0 {
		return errors.New("League replay process ID must be positive")
	}
	if post == nil {
		return errors.New("League replay key poster is not configured")
	}
	if err := post(pid, virtualKey); err != nil {
		return fmt.Errorf("post virtual key 0x%04x to League replay process %d: %w", virtualKey, pid, err)
	}
	return nil
}
