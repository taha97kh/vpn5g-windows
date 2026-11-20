//go:build windows

package main

import "os/exec"

// launchUpdater starts the updater script detached so it survives this
// process exiting (cmd /C start detaches it).
func launchUpdater(batPath string) error {
	return exec.Command("cmd", "/C", "start", "/min", "", batPath).Start()
}
