//go:build windows

package main

import (
	"log"

	"github.com/getlantern/systray"
)

// tray.go — the Windows system tray (v2.0.0). No console window anymore
// (the binary is built with -H=windowsgui); the tray is the app's face
// when the browser UI is closed: connect/disconnect, open the UI, quit.
// The icon is the transparent 512 shield artwork converted to .ico and
// embedded at build time (icon.ico, generated from the Android artwork).

func runTray(conn *connection, onQuit func()) chan struct{} {
	quit := make(chan struct{})
	go func() {
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetTitle("VPN 5G")
			systray.SetTooltip("VPN 5G — 4ref.ai")
			mConnect := systray.AddMenuItem("اتصال", "Connect to the fastest server")
			mDisconnect := systray.AddMenuItem("قطع اتصال", "Disconnect")
			mOpen := systray.AddMenuItem("باز کردن برنامه", "Open the VPN 5G window")
			systray.AddSeparator()
			mQuit := systray.AddMenuItem("خروج", "Quit VPN 5G")
			go func() {
				for {
					select {
					case <-mConnect.ClickedCh:
						// The forced-ad rule applies here too: when an ad
						// is due, open the browser UI so the user sees
						// the interstitial and connects from there. The
						// tray never bypasses the ad.
						if ad := pickAd(subCache.get()); ad != nil {
							_ = openBrowser("http://127.0.0.1:17890")
							continue
						}
						adGate.noteUserConnectAttempt()
						if err := conn.connect("", true); err != nil {
							log.Printf("tray connect: %v", err)
						}
					case <-mDisconnect.ClickedCh:
						conn.disconnect()
					case <-mOpen.ClickedCh:
						_ = openBrowser("http://127.0.0.1:17890")
					case <-mQuit.ClickedCh:
						systray.Quit()
						onQuit()
						return
					case <-quit:
						return
					}
				}
			}()
		}, func() {
			close(quit)
		})
	}()
	return quit
}
