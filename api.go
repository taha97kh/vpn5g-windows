package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

const telegramBotURL = "https://t.me/VPN5G_IRBot"

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func registerAPI(mux *http.ServeMux, store *serverStore, conn *connection, auth *authStore) {
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		count, updatedAt, refreshing := store.status()
		st := conn.status()
		st["servers_count"] = count
		st["servers_updated_at"] = updatedAt.Format(time.RFC3339)
		st["servers_refreshing"] = refreshing
		st["version"] = appVersion
		st["update_available"] = upd.snapshot()["update_available"]
		st["latest_version"] = upd.snapshot()["latest_version"]
		writeJSON(w, http.StatusOK, st)
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"version":         appVersion,
			"proxy_port":      proxyPort,
			"ui_port":         uiPort,
			"session_minutes": sessionMinutes,
			"free_limit":      freeLimit,
			"telegram":        telegramBotURL,
		})
	})

	mux.HandleFunc("/api/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		count, updatedAt, refreshing := store.status()
		writeJSON(w, http.StatusOK, map[string]any{
			"servers":          store.getServers(),
			"count":            count,
			"updated_at":       updatedAt.Format(time.RFC3339),
			"refreshing":       refreshing,
			"refresh_interval": "5m",
		})
	})

	mux.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			ServerID string `json:"server_id"`
		}
		if err := decodeJSON(r, &req); err != nil || req.ServerID == "" {
			writeErr(w, http.StatusBadRequest, "server_id required")
			return
		}
		e := store.getByID(req.ServerID)
		if e == nil {
			writeErr(w, http.StatusNotFound, "server not found")
			return
		}
		if !e.Free {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"ok": false, "error": "vip",
				"message":  "This server is VIP. Get VIP in the Telegram bot.",
				"telegram": telegramBotURL,
			})
			return
		}
		if err := conn.connect(req.ServerID); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": conn.status()})
	})

	mux.HandleFunc("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		conn.disconnect()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": conn.status()})
	})

	// --- Auth (Supabase email OTP) ---
	mux.HandleFunc("/api/auth/send-code", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			Email string `json:"email"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := sendOTP(req.Email); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			Email string `json:"email"`
			Code  string `json:"code"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := auth.verifyOTP(req.Email, req.Code); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": auth.signedInEmail()})
	})

	mux.HandleFunc("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		email := auth.signedInEmail()
		writeJSON(w, http.StatusOK, map[string]any{"signed_in": email != "", "email": email})
	})

	mux.HandleFunc("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		auth.logout()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// --- In-app update ---
	mux.HandleFunc("/api/update/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		checkForUpdate(r.URL.Query().Get("force") == "1")
		writeJSON(w, http.StatusOK, upd.snapshot())
	})

	mux.HandleFunc("/api/update/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		writeJSON(w, http.StatusOK, upd.snapshot())
	})

	mux.HandleFunc("/api/update/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if err := startDownload(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": updateDownloading})
	})

	mux.HandleFunc("/api/update/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if err := applyUpdate(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarting": true})
		// Exit promptly so the updater can replace the files. Flush the
		// response first, then disconnect (stop xray, restore proxy).
		go func() {
			time.Sleep(600 * time.Millisecond)
			conn.disconnect()
			os.Exit(0)
		}()
	})
}
