//go:build !windows

package main

// runTray is a no-op off Windows: the tray only exists in the Windows
// build (systray's Windows backend needs no CGO, so the cross-compile
// from Linux stays CGO_ENABLED=0).
func runTray(conn *connection, onQuit func()) chan struct{} {
	return make(chan struct{})
}
