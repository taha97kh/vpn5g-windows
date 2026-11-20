//go:build !windows

package main

// Non-Windows stub: the system proxy can only be set on Windows. The Linux
// build is for cross-compilation checks only; proxy calls are no-ops.
func setSystemProxy(on bool) error { return nil }
