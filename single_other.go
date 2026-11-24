//go:build !windows

package main

// ensureSingleInstance is a no-op off Windows (dev/CI builds only); the
// real guard lives in single_windows.go.
func ensureSingleInstance() bool { return false }
