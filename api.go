package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const telegramBotURL = "https://t.me/VPN5G_IRBot"

func buyURL(planID string) string {
	return telegramBotURL + "?start=buy_" + strings.TrimSpace(planID)
}

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

// redeemedCodes remembers codes redeemed on this device (email + time) so
// the config-list fallback never activates the same code twice.
type redeemedStore struct {
	mu   sync.Mutex
	rows map[string]string
}

var redeemed = &redeemedStore{rows: map[string]string{}}

func redeemedFilePath() (string, error) {
	dir, err := appDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "redeemed.json"), nil
}

func (s *redeemedStore) load() {
	p, err := redeemedFilePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err == nil && m != nil {
		s.mu.Lock()
		s.rows = m
		s.mu.Unlock()
	}
}

func (s *redeemedStore) save() {
	s.mu.Lock()
	m := s.rows
	s.mu.Unlock()
	p, err := redeemedFilePath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	data, _ := json.Marshal(m)
	_ = os.WriteFile(p, data, 0o600)
}

func (s *redeemedStore) get(code string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.rows[strings.ToUpper(strings.TrimSpace(code))]
	return v, ok
}

func (s *redeemedStore) set(code, email string) {
	s.mu.Lock()
	s.rows[strings.ToUpper(strings.TrimSpace(code))] = email
	s.mu.Unlock()
	s.save()
}

func planDays(planID string) int {
	for _, p := range remoteCfg.plans() {
		if p.ID == planID && p.Days > 0 {
			return p.Days
		}
	}
	switch planID {
	case "sub_1":
		return 30
	case "sub_3":
		return 90
	case "sub_6":
		return 180
	case "sub_12":
		return 360
	}
	return 0
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
		st["volume_exceeded"] = usage.exceeded(subCache.get())
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
			"stats_port":      xrayStatsPort,
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
		allowed := map[string]bool{}
		for _, e := range conn.allowedServers() {
			allowed[e.ID] = true
		}
		servers := store.getServers()
		out := make([]map[string]any, 0, len(servers))
		for _, e := range servers {
			out = append(out, map[string]any{
				"id": e.ID, "protocol": e.Protocol, "country": e.Country,
				"name": e.Name, "uri": e.URI, "host": e.Host, "port": e.Port,
				"ping_ms": e.PingMs, "free": e.Free, "allowed": allowed[e.ID],
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"servers":          out,
			"count":            count,
			"updated_at":       updatedAt.Format(time.RFC3339),
			"refreshing":       refreshing,
			"refresh_interval": "5m",
		})
	})

	// /api/connect — the local UI and the Chrome extension both use it.
	// Body: {"server_id": "..."} (local UI) or {"uri": "..."} (extension,
	// which picked the server itself) or {"via": "extension"} to skip the
	// helper's own ad gate (the extension shows its own interstitial).
	mux.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			ServerID string `json:"server_id"`
			URI      string `json:"uri"`
			Via      string `json:"via"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		sub := subCache.get()
		// Forced-ad gate (local UI only; the extension enforces its own ad).
		if req.Via != "extension" {
			if ad := pickAd(sub); ad != nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": false, "error": "ad_required", "ad": ad,
					"duration_seconds": ad.Duration,
				})
				return
			}
		}
		adGate.noteUserConnectAttempt()
		if req.URI != "" {
			e, err := parseLink(strings.TrimSpace(req.URI))
			if err != nil || e == nil {
				writeErr(w, http.StatusBadRequest, "invalid server link")
				return
			}
			if err := conn.connectURI(e); err != nil {
				if err.Error() == "vip" {
					writeJSON(w, http.StatusForbidden, map[string]any{
						"ok": false, "error": "vip",
						"message":  "This server is VIP. Get VIP in the Telegram bot.",
						"telegram": telegramBotURL,
					})
					return
				}
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": conn.status()})
			return
		}
		if req.ServerID == "" {
			// No server chosen: connect to the fastest allowed server.
			if err := conn.connect("", true); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": conn.status()})
			return
		}
		e := store.getByID(req.ServerID)
		if e == nil {
			writeErr(w, http.StatusNotFound, "server not found")
			return
		}
		if !conn.isAllowed(req.ServerID) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"ok": false, "error": "vip",
				"message":  "This server is VIP. Get VIP in the Telegram bot.",
				"telegram": telegramBotURL,
			})
			return
		}
		if err := conn.connect(req.ServerID, true); err != nil {
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

	// POST /api/quit — clean shutdown for the uninstaller (and the
	// Native Messaging host): disconnect the tunnel, then exit so the
	// installer/uninstaller can replace or remove the files. The server
	// only listens on 127.0.0.1, so this is loopback-only.
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		// Flush the response first, then disconnect (stop xray, restore
		// the system proxy) and exit.
		go func() {
			time.Sleep(300 * time.Millisecond)
			conn.disconnect()
			os.Exit(0)
		}()
	})

	// GET /api/stats — live tunnel speeds (Chrome extension helper contract).
	// Unavailable speeds are reported as available=false; the UI shows "—",
	// never a fake zero.
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		sp := speeds.snapshot()
		st := conn.status()
		writeJSON(w, http.StatusOK, map[string]any{
			"connected":  st["connected"],
			"server_id":  st["server_id"],
			"down_bps":   sp.DownBps,
			"up_bps":     sp.UpBps,
			"down_total": sp.DownTotal,
			"up_total":   sp.UpTotal,
			"available":  sp.Available,
		})
	})

	// GET /api/subscription — the effective subscription (server truth
	// merged with the local cache). Refreshes from the server in the
	// background; never blocks the UI on a failed fetch.
	mux.HandleFunc("/api/subscription", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		email := auth.signedInEmail()
		if email != "" {
			go auth.refreshSubscription()
		}
		sub := subCache.get()
		// Volume (Model B): cap from the server-published plan ("gb"),
		// usage from the real local tunnel counters. Nulls when the plan
		// has no cap — the UI shows «نامحدود», never a made-up number.
		vol := usage.snapshot(sub)
		var volumeGB, remainingGB, usagePct *float64
		if vol.Limited {
			g := vol.CapGB
			volumeGB = &g
			rg := float64(vol.RemainingBytes) / float64(bytesPerGB)
			remainingGB = &rg
			p := vol.Percent
			usagePct = &p
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"signed_in":       email != "",
			"email":           email,
			"status":          sub.Status,
			"plan":            sub.Plan,
			"subscriber":      sub.active() && !sub.isGift(),
			"gift":            sub.isGift(),
			"remaining_days":  sub.remainingDays(),
			"expires_at":      sub.ExpiresAt.Format(time.RFC3339),
			"volume_limited":  vol.Limited,
			"volume_gb":       volumeGB,
			"used_bytes":      vol.UsedBytes,
			"used_gb":         float64(vol.UsedBytes) / float64(bytesPerGB),
			"remaining_gb":    remainingGB,
			"usage_percent":   usagePct,
			"volume_exceeded": vol.Exceeded,
		})
	})

	// GET /api/plans — subscription plans + prices from the server config.
	mux.HandleFunc("/api/plans", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		plans := remoteCfg.plans()
		out := make([]map[string]any, 0, len(plans))
		for _, p := range plans {
			out = append(out, map[string]any{
				"id": p.ID, "days": p.Days, "price_toman": p.PriceToman,
				"name_fa": p.NameFa, "gb": p.GB, "buy_url": buyURL(p.ID),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"plans": out})
	})

	// GET /api/announcement — the live admin announcement (or null).
	mux.HandleFunc("/api/announcement", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		ann := remoteCfg.announcement()
		if ann == nil {
			writeJSON(w, http.StatusOK, map[string]any{"announcement": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"announcement": map[string]any{
			"id": ann.ID, "text": ann.Text, "image_url": ann.ImageURL, "link_url": ann.LinkURL,
		}})
	})

	// POST /api/redeem — register a purchased subscription code (Premium,
	// top of the screen) or a gift code (All Locations). Server-first:
	// validate_and_redeem_code; when the RPC is unavailable the published
	// config list is the fallback. Requires sign-in.
	mux.HandleFunc("/api/redeem", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			Code string `json:"code"`
		}
		if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Code) == "" {
			writeErr(w, http.StatusBadRequest, "code required")
			return
		}
		email := auth.signedInEmail()
		if email == "" {
			writeErr(w, http.StatusUnauthorized, "ابتدا وارد حساب شوید")
			return
		}
		code := strings.ToUpper(strings.TrimSpace(req.Code))
		status, plan, expiresAt, available := auth.validateAndRedeemCode(code, email)
		if available {
			switch status {
			case "ok", "valid", "validated":
				sub := subscriptionFromServer(plan, expiresAt)
				subCache.set(sub)
				go auth.recordCodeRedemption(code, email)
				redeemed.set(code, email)
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": true, "plan": sub.Plan, "status": sub.Status,
					"expires_at":     sub.ExpiresAt.Format(time.RFC3339),
					"remaining_days": sub.remainingDays(),
				})
				return
			case "unknown":
				writeErr(w, http.StatusNotFound, "چنین کدی وجود ندارد")
				return
			case "already_used":
				writeErr(w, http.StatusConflict, "این کد قبلاً استفاده شده است")
				return
			case "expired":
				writeErr(w, http.StatusGone, "این کد منقضی شده است")
				return
			case "exhausted":
				writeErr(w, http.StatusConflict, "ظرفیت این کد تکمیل شده است")
				return
			case "not_yours":
				writeErr(w, http.StatusForbidden, "این کد برای حساب دیگری صادر شده است")
				return
			}
		}
		// Fallback: the published config list.
		entry := remoteCfg.findCode(code)
		if entry == nil {
			writeErr(w, http.StatusNotFound, "چنین کدی وجود ندارد")
			return
		}
		if entry.ExpiresAt != "" {
			if exp := parseExpiresAt(entry.ExpiresAt); !exp.IsZero() && time.Now().After(exp) {
				writeErr(w, http.StatusGone, "این کد منقضی شده است")
				return
			}
		}
		if entry.RemainingUses <= 0 && entry.MaxUses > 0 {
			writeErr(w, http.StatusConflict, "ظرفیت این کد تکمیل شده است")
			return
		}
		if prev, ok := redeemed.get(code); ok && prev != email {
			writeErr(w, http.StatusForbidden, "این کد قبلاً با حساب دیگری فعال شده است")
			return
		}
		sub := subscriptionFromCode(entry)
		subCache.set(sub)
		redeemed.set(code, email)
		go auth.recordCodeRedemption(code, email)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "plan": sub.Plan, "status": sub.Status,
			"expires_at":     sub.ExpiresAt.Format(time.RFC3339),
			"remaining_days": sub.remainingDays(),
		})
	})

	// POST /api/ads/viewed — the interstitial countdown completed. Marks
	// the current connect attempt as ad-viewed (so the retry connects)
	// and records the impression + server view (best effort).
	mux.HandleFunc("/api/ads/viewed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			AdID string `json:"ad_id"`
		}
		_ = decodeJSON(r, &req)
		adGate.markAdViewed()
		recordAdShown(req.AdID, auth.signedInEmail(), auth)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// POST /api/ads/click — a tap on the ad (text/image/button). Records
	// the click server-side (best effort). A tap never ends the countdown.
	mux.HandleFunc("/api/ads/click", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			AdID string `json:"ad_id"`
		}
		_ = decodeJSON(r, &req)
		if req.AdID != "" {
			go auth.recordAdClick(req.AdID, auth.signedInEmail())
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// --- Auth (Supabase, password-only; OTP removed) ---
	mux.HandleFunc("/api/auth/signup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := auth.signup(req.Email, req.Password); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": auth.signedInEmail()})
	})

	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := auth.login(req.Email, req.Password); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": auth.signedInEmail()})
	})

	mux.HandleFunc("/api/auth/recover", func(w http.ResponseWriter, r *http.Request) {
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
		if err := sendRecoveryLink(req.Email); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"note": "لینک بازنشانی به ایمیلت ارسال شد؛ آن را در مرورگر باز کن و رمز جدید را تعیین کن.",
		})
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

// subscriptionFromServer builds the local subscription from a server-side
// validation result (plan + expires_at from the RPC).
func subscriptionFromServer(plan string, expiresAt time.Time) subscriptionInfo {
	if plan == "" {
		plan = "sub_12"
	}
	if expiresAt.IsZero() {
		days := planDays(plan)
		if days <= 0 {
			days = 30
		}
		expiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour)
	}
	return subscriptionInfo{Status: "active", Plan: plan, ExpiresAt: expiresAt}
}

// subscriptionFromCode builds the local subscription from a published
// config-list code entry (the fallback path).
func subscriptionFromCode(entry *codeRemote) subscriptionInfo {
	now := time.Now()
	if entry.Plan != "" {
		days := planDays(entry.Plan)
		if days <= 0 {
			days = 30
		}
		exp := now.Add(time.Duration(days) * 24 * time.Hour)
		if e := parseExpiresAt(entry.ExpiresAt); !e.IsZero() && e.Before(exp) {
			exp = e
		}
		return subscriptionInfo{Status: "active", Plan: entry.Plan, ExpiresAt: exp}
	}
	hours := entry.GiftHours
	if hours <= 0 && entry.GiftDays > 0 {
		hours = entry.GiftDays * 24
	}
	if hours <= 0 {
		hours = 24
	}
	exp := now.Add(time.Duration(hours) * time.Hour)
	if e := parseExpiresAt(entry.ExpiresAt); !e.IsZero() && e.Before(exp) {
		exp = e
	}
	return subscriptionInfo{Status: "gift", Plan: "gift", ExpiresAt: exp}
}
