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
	appVersion     = "v1.0.0"
	proxyPort      = 10809
	uiPort         = 17890
	sessionMinutes = 30
	freeLimit      = 20
)

//go:embed web
var webFS embed.FS

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("VPN 5G for Windows %s starting", appVersion)

	store := newServerStore()
	conn := newConnection(store)
	auth := newAuthStore()

	go store.refreshLoop()
	go updateCheckLoop()

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
