//go:build windows

package main

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const (
	internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	proxyServerValue    = "127.0.0.1:10809"
)

// setSystemProxy enables (or disables) the Windows system proxy pointing at
// the local Xray HTTP inbound. No admin rights needed: this is HKCU.
func setSystemProxy(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Internet Settings key: %w", err)
	}
	defer k.Close()
	if on {
		if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
			return err
		}
		if err := k.SetStringValue("ProxyServer", proxyServerValue); err != nil {
			return err
		}
	} else {
		if err := k.SetDWordValue("ProxyEnable", 0); err != nil {
			return err
		}
	}
	notifyProxyChange()
	return nil
}

// notifyProxyChange tells Windows (and running apps) the proxy settings
// changed, via wininet InternetSetOption.
func notifyProxyChange() {
	wininet := syscall.NewLazyDLL("wininet.dll")
	setOption := wininet.NewProc("InternetSetOptionW")
	const (
		internetOptionSettingsChanged = 39
		internetOptionRefresh         = 37
	)
	_, _, _ = setOption.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = setOption.Call(0, internetOptionRefresh, 0, 0)
}
