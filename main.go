package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	appVersion     = "v2.1.0"
	proxyPort      = 10809
	uiPort         = 17890
	sessionMinutes = 30
	freeLimit      = 20
)

//go:embed web
var webFS embed.FS

//go:embed icon.ico
var trayIcon []byte

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("VPN 5G for Windows %s starting", appVersion)

	// Second copy? Never start a second tunnel / second HTTP server:
	// just open the running instance's UI and exit.
	if ensureSingleInstance() {
		log.Println("another instance is already running — opening its window and exiting")
		_ = openBrowser("http://127.0.0.1:17890")
		return
	}

	subCache.load()
	adImpressions.load()
	redeemed.load()
	usage.load()

	store := newServerStore()
	auth := newAuthStore()
	conn := newConnection(store, auth)

	// Model B, volume half: cut the tunnel as soon as a volume-capped
	// plan runs out mid-session.
	go volumeWatchdog(conn)

	go store.refreshLoop()
	go remoteCfg.refreshLoop()
	go updateCheckLoop()
	go func() {
		// Refresh the subscription shortly after start (server truth).
		time.Sleep(2 * time.Second)
		auth.refreshSubscription()
	}()

	mux := http.NewServeMux()
	registerAPI(mux, store, conn, auth)

	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("web assets: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(webRoot)))

	srv := &http.Server{Addr: "127.0.0.1:17890", Handler: mux}

	// Open the UI in the default browser shortly after start.
	// VPN5G_NO_BROWSER=1 skips this (used by the CI smoke test).
	if os.Getenv("VPN5G_NO_BROWSER") == "" {
		go func() {
			time.Sleep(400 * time.Millisecond)
			if err := openBrowser("http://127.0.0.1:17890"); err != nil {
				log.Printf("open browser: %v", err)
			}
		}()
	}

	// System tray (Windows only; a no-op elsewhere). The tray owns the
	// process lifetime on Windows: choosing Quit there stops the tunnel
	// and exits. The UI stays available at 127.0.0.1:17890.
	trayQuit := runTray(conn, func() {
		log.Println("tray quit requested")
		conn.disconnect()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		os.Exit(0)
	})
	_ = trayQuit

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Println("shutting down")
		conn.disconnect()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		os.Exit(0)
	}()

	log.Printf("UI at http://127.0.0.1:%d", uiPort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
	conn.disconnect()
}
