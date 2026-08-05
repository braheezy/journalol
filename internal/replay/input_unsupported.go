//go:build !darwin || !cgo

package replay

import "errors"

func platformPostKey(_ int, _ uint16) error {
	return errors.New("synthetic replay keyboard input requires a macOS host build with cgo enabled")
}

func platformEnsureInputAccess() error {
	return errors.New("synthetic replay keyboard input requires a macOS host build with cgo enabled")
}
