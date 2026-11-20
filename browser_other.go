//go:build !windows

package main

// Non-Windows stub: opening a browser is a Windows-only launch step.
func openBrowser(url string) error { return nil }
