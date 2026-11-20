//go:build !windows

package main

import "errors"

// Non-Windows stub: applying an update only makes sense on Windows.
func launchUpdater(batPath string) error {
	return errors.New("updates can only be applied on Windows")
}
