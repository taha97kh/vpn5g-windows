//go:build windows

package main

import (
	"log"

	"golang.org/x/sys/windows"
)

// singleInstanceMutexName is shared with the installer's AppMutex: while
// the app runs, the installer refuses to overwrite its files.
const singleInstanceMutexName = "TAHA VPN 5G Single Instance"

var singleInstanceHandle windows.Handle

// ensureSingleInstance reports whether another copy of the app is already
// running. The first instance owns a named mutex for the life of the
// process; a second instance never starts a second tunnel or a second
// HTTP server — main() opens the first instance's UI and exits instead.
func ensureSingleInstance() bool {
	name, err := windows.UTF16PtrFromString(singleInstanceMutexName)
	if err != nil {
		return false
	}
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		return true
	}
	if err != nil {
		log.Printf("single-instance mutex: %v", err)
		return false
	}
	singleInstanceHandle = h
	return false
}
