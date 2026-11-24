package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// remoteConfig.go — the app-side mirror of the bot-published app_config.json
// (same contract as the Android app, docs/app_config.md). Fetched on start
// and every 5 minutes. Safe defaults everywhere: a missing field, a 404,
// or a network failure never crashes the app and never hides the servers —
// it just means "no plans / no codes / no ads yet".

type planRemote struct {
	ID         string `json:"id"`
	Days       int    `json:"days"`
	PriceToman int64  `json:"price_toman"`
	NameFa     string `json:"name_fa"`
}

type codeRemote struct {
	Code          string `json:"code"`
	Type          string `json:"type"` // "gift" or "sub_*"
	Plan          string `json:"plan"`
	Tier          string `json:"tier"` // "gift" (middle tier) or a plan id
	GiftHours     int    `json:"gift_hours"`
	GiftDays      int    `json:"gift_days"`
	MaxUses       int    `json:"max_uses"`
	RemainingUses int    `json:"remaining_uses"`
	ExpiresAt     string `json:"expires_at"`
}

type announcementRemote struct {
	Enabled  bool   `json:"enabled"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
	LinkURL  string `json:"link_url"`
	ID       string `json:"id"`
}

type interstitialRemote struct {
	Enabled            bool   `json:"enabled"`
	DurationSeconds    int    `json:"duration_seconds"`         // 1..20, absent = 10
	ShowFromConnectNum int    `json:"show_from_connect_number"` // default 2
	AppliesToFreeOnly  *bool  `json:"applies_to_free_only"`     // default true
	Text               string `json:"text"`
	ImageURL           string `json:"image_url"`
	LinkURL            string `json:"link_url"`
	ID                 string `json:"id"`
}

type userAdRemote struct {
	ID                string   `json:"id"`
	Text              string   `json:"text"`
	ImageURL          string   `json:"image_url"`
	LinkURL           string   `json:"link_url"`
	Model             string   `json:"model"` // once | ten_times | hourly | monthly_daily
	MaxImpressions    int      `json:"max_impressions"`
	ImpressionsPerDay *int     `json:"impressions_per_day"`
	HourWindows       [][2]int `json:"hour_windows"` // [start, end), end-exclusive
	PriceToman        int64    `json:"price_toman"`
	Enabled           bool     `json:"enabled"`
	Approved          bool     `json:"approved"`
}

type adPricingRemote struct {
	OnceToman         int64 `json:"once_toman"`
	TenTimesToman     int64 `json:"ten_times_toman"`
	HourlyToman       int64 `json:"hourly_toman"`
	MonthlyDailyToman int64 `json:"monthly_daily_toman"`
}

type adsRemote struct {
	Interstitial interstitialRemote `json:"interstitial"`
	UserAds      []userAdRemote     `json:"user_ads"`
	Pricing      adPricingRemote    `json:"pricing"`
}

type appConfigRemote struct {
	Plans         []planRemote        `json:"plans"`
	Codes         []codeRemote        `json:"codes"`
	Announcement  *announcementRemote `json:"announcement"`
	Ads           *adsRemote          `json:"ads"`
	ConfigSources []string            `json:"config_sources"`
}

// remoteConfig holds the latest fetched app_config.json (or the safe
// defaults when nothing was ever fetched).
type remoteConfig struct {
	mu      sync.RWMutex
	cfg     appConfigRemote
	fetched time.Time
	ok      bool
}

var remoteCfg = &remoteConfig{}

func (r *remoteConfig) snapshot() (appConfigRemote, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg, r.ok
}

func (r *remoteConfig) plans() []planRemote {
	cfg, _ := r.snapshot()
	return cfg.Plans
}

func (r *remoteConfig) codes() []codeRemote {
	cfg, _ := r.snapshot()
	return cfg.Codes
}

func (r *remoteConfig) announcement() *announcementRemote {
	cfg, _ := r.snapshot()
	if cfg.Announcement == nil || !cfg.Announcement.Enabled {
		return nil
	}
	return cfg.Announcement
}

func (r *remoteConfig) ads() *adsRemote {
	cfg, _ := r.snapshot()
	return cfg.Ads
}

func (r *remoteConfig) fetch() {
	resp, err := httpClient.Get(appConfigURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var cfg appConfigRemote
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		log.Printf("remote config: parse error: %v", err)
		return
	}
	r.mu.Lock()
	r.cfg = cfg
	r.fetched = time.Now()
	r.ok = true
	r.mu.Unlock()
}

func (r *remoteConfig) refreshLoop() {
	r.fetch()
	for range time.Tick(refreshInterval) {
		r.fetch()
	}
}

// findCode returns the published code entry with this code (case-insensitive),
// or nil. The config list is the FALLBACK when the server-side
// validate_and_redeem_code RPC is unavailable (see auth.go) — never the
// authority when the RPC answers.
func (r *remoteConfig) findCode(code string) *codeRemote {
	for i := range r.codes() {
		if equalFold(r.codes()[i].Code, code) {
			c := r.codes()[i]
			return &c
		}
	}
	return nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'a' && ca <= 'z' {
			ca -= 'a' - 'A'
		}
		if cb >= 'a' && cb <= 'z' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
