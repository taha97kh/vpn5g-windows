package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ads.go — the 1.1.15 ad contract, Windows side (same rules as Android).
//
// FORCED-AD RULE: connect #1 = NO forced ad. From connect #2 on (each
// after a disconnect), the full-screen interstitial shows BEFORE the VPN
// starts. Countdown 1–20 s (admin-set, default 10). The VPN starts ONLY
// after the full view; an early close = INCOMPLETE, no connect. Only
// user-initiated connects count (auto-reconnect and failover restarts
// never count and never show an ad). Subscribers and the gift tier skip
// the ad by default (applies_to_free_only).
//
// A due user ad is picked before the admin interstitial; user ads use the
// admin-set duration. Impressions are capped at the purchased amounts —
// never more than bought. Views are recorded server-side (best effort);
// the server timestamps them, never the local clock.

type adLedgerEntry struct {
	Count         int            `json:"count"`
	Days          map[string]int `json:"days"`
	LastShownUnix int64          `json:"last_shown_unix"`
}

type adLedger struct {
	mu      sync.Mutex
	entries map[string]*adLedgerEntry
}

var adImpressions = &adLedger{entries: map[string]*adLedgerEntry{}}

func adLedgerFilePath() (string, error) {
	dir, err := appDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ad_impressions.json"), nil
}

func (l *adLedger) load() {
	p, err := adLedgerFilePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var m map[string]*adLedgerEntry
	if err := json.Unmarshal(data, &m); err == nil && m != nil {
		l.mu.Lock()
		l.entries = m
		l.mu.Unlock()
	}
}

func (l *adLedger) save() {
	l.mu.Lock()
	m := l.entries
	l.mu.Unlock()
	p, err := adLedgerFilePath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	data, _ := json.Marshal(m)
	_ = os.WriteFile(p, data, 0o600)
}

func (l *adLedger) count(adID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[adID]; ok {
		return e.Count
	}
	return 0
}

func (l *adLedger) dayCount(adID string, day string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[adID]; ok && e.Days != nil {
		return e.Days[day]
	}
	return 0
}

func (l *adLedger) record(adID string) {
	l.mu.Lock()
	e, ok := l.entries[adID]
	if !ok {
		e = &adLedgerEntry{Days: map[string]int{}}
		l.entries[adID] = e
	}
	if e.Days == nil {
		e.Days = map[string]int{}
	}
	e.Count++
	e.Days[time.Now().Format("2006-01-02")]++
	e.LastShownUnix = time.Now().Unix()
	l.mu.Unlock()
	l.save()
}

// adState tracks connect counting for the forced-ad rule.
type adState struct {
	mu              sync.Mutex
	connectAttempts int  // user-initiated connects only
	adViewed        bool // the current attempt already watched its ad
}

var adGate = &adState{}

func (s *adState) noteUserConnectAttempt() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connectAttempts++
	s.adViewed = false
	return s.connectAttempts
}

func (s *adState) markAdViewed() {
	s.mu.Lock()
	s.adViewed = true
	s.mu.Unlock()
}

func (s *adState) viewed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adViewed
}

func (s *adState) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectAttempts
}

// adChoice is what /api/connect returns when an ad must be shown first.
type adChoice struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
	LinkURL  string `json:"link_url"`
	Duration int    `json:"duration_seconds"`
	IsUserAd bool   `json:"is_user_ad"`
}

func clampDuration(d int) int {
	if d < 1 {
		return 10
	}
	if d > 20 {
		return 20
	}
	return d
}

func inHourWindows(windows [][2]int, now time.Time) bool {
	if len(windows) == 0 {
		return true
	}
	h := now.Hour()
	for _, w := range windows {
		if h >= w[0] && h < w[1] {
			return true
		}
	}
	return false
}

func userAdDue(ad userAdRemote, now time.Time) bool {
	if !ad.Enabled || !ad.Approved || ad.ID == "" {
		return false
	}
	maxImp := ad.MaxImpressions
	if maxImp <= 0 {
		switch ad.Model {
		case "once":
			maxImp = 1
		case "ten_times":
			maxImp = 10
		case "monthly_daily":
			maxImp = 150 // 30 days x 5/day
		default:
			maxImp = 1
		}
	}
	if adImpressions.count(ad.ID) >= maxImp {
		return false
	}
	perDay := 0
	if ad.ImpressionsPerDay != nil {
		perDay = *ad.ImpressionsPerDay
	} else if ad.Model == "monthly_daily" {
		perDay = 5
	}
	if perDay > 0 && adImpressions.dayCount(ad.ID, now.Format("2006-01-02")) >= perDay {
		return false
	}
	return inHourWindows(ad.HourWindows, now)
}

// pickAd returns the ad to show before this connect (nil = no ad).
// A due user ad wins over the admin interstitial. Subscribers and the
// gift tier skip when applies_to_free_only (the default).
func pickAd(sub subscriptionInfo) *adChoice {
	ads := remoteCfg.ads()
	if ads == nil {
		return nil
	}
	inter := ads.Interstitial
	if !inter.Enabled {
		return nil
	}
	// Subscribers + gift skip by default.
	if inter.AppliesToFreeOnly == nil || *inter.AppliesToFreeOnly {
		if sub.active() {
			return nil
		}
	}
	from := inter.ShowFromConnectNum
	if from <= 0 {
		from = 2
	}
	if adGate.attempts() < from {
		return nil
	}
	if adGate.viewed() {
		return nil
	}
	duration := clampDuration(inter.DurationSeconds)
	now := time.Now()
	for _, ua := range ads.UserAds {
		if userAdDue(ua, now) {
			return &adChoice{
				ID: ua.ID, Text: ua.Text, ImageURL: ua.ImageURL,
				LinkURL: ua.LinkURL, Duration: duration, IsUserAd: true,
			}
		}
	}
	id := inter.ID
	if id == "" {
		id = "admin-interstitial"
	}
	return &adChoice{
		ID: id, Text: inter.Text, ImageURL: inter.ImageURL,
		LinkURL: inter.LinkURL, Duration: duration, IsUserAd: false,
	}
}

// recordAdShown stores the impression locally and reports the view
// server-side (best effort). Called only after the countdown completed.
func recordAdShown(adID, email string, auth *authStore) {
	if adID == "" {
		return
	}
	adImpressions.record(adID)
	if auth != nil {
		go auth.recordAdView(adID, email)
	}
	log.Printf("ad shown: %s", adID)
}
