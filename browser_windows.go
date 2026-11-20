//go:build windows

package main

import "os/exec"

// openBrowser opens a URL in the default browser via rundll32.
func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
