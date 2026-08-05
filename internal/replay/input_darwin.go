//go:build darwin && cgo

package replay

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>
#include <stdint.h>
#include <sys/types.h>

static int journalol_preflight_post_event_access(void) {
	return CGPreflightPostEventAccess() ? 1 : 0;
}

static int journalol_request_post_event_access(void) {
	return CGRequestPostEventAccess() ? 1 : 0;
}

// Returns 0 on success, 1 if the key-down event could not be created, and 2
// if the key-up event could not be created. CGEventPostToPid itself has no
// return value.
static int journalol_post_key(pid_t pid, uint16_t virtual_key) {
	CGEventRef key_down = CGEventCreateKeyboardEvent(
		NULL, (CGKeyCode)virtual_key, true);
	if (key_down == NULL) {
		return 1;
	}

	CGEventRef key_up = CGEventCreateKeyboardEvent(
		NULL, (CGKeyCode)virtual_key, false);
	if (key_up == NULL) {
		CFRelease(key_down);
		return 2;
	}

	CGEventSetFlags(key_down, 0);
	CGEventSetFlags(key_up, 0);
	CGEventPostToPid(pid, key_down);
	CGEventPostToPid(pid, key_up);

	CFRelease(key_down);
	CFRelease(key_up);
	return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
)

func platformPostKey(pid int, virtualKey uint16) error {
	if err := platformEnsureInputAccess(); err != nil {
		return err
	}

	switch result := int(C.journalol_post_key(C.pid_t(pid), C.uint16_t(virtualKey))); result {
	case 0:
		return nil
	case 1:
		return errors.New("create key-down event")
	case 2:
		return errors.New("create key-up event")
	default:
		return fmt.Errorf("post keyboard event: unexpected result %d", result)
	}
}

func platformEnsureInputAccess() error {
	if C.journalol_preflight_post_event_access() != 0 ||
		C.journalol_request_post_event_access() != 0 {
		return nil
	}
	return errors.New("macOS denied keyboard-event access; enable the terminal or app running Journalol in System Settings > Privacy & Security > Accessibility, then retry")
}
