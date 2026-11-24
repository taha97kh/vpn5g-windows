package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// auth.go — Supabase Auth for the Windows app (mirrors Android 1.1.9+).
//
// NEW project (vpn5g.irbot@gmail.com's Project, ref atvcpfnnmwguhjvhbbfj).
// The anon key below is a PUBLIC client key by design — it is safe to ship,
// and the Android app ships the same one. NEVER put a service_role key or
// DB password here.
//
// Registration is password-only, one account per IP + email:
// fetch the public IP from the get-client-ip Edge Function, then
// check_registration, then signUp, then register_account (best effort;
// UNIQUE(ip) is the race backstop). The IP guard applies ONLY to signup.
// Password login never checks the IP. OTP is gone (removed in Android
// 1.1.9) — there are no OTP endpoints here.
//
// Password recovery is the LINK flow: /auth/v1/recover sends a link whose
// redirect is the reset page on the HF static site
// (https://taha97kh-vpn5g-app.static.hf.space/reset.html). HONEST LIMIT:
// a portable exe has no vpn5g:// handler, so the link completes in the
// browser on that page — the page itself is published by the bot side
// (HF Space content). Until it exists, the app says so plainly instead
// of pretending the reset works in-app.

const (
	supabaseURL     = "https://atvcpfnnmwguhjvhbbfj.supabase.co"
	supabaseAnonKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6ImF0dmNwZm5ubXdndWhqdmhiYmZqIiwicm9sZSI6ImFub24iLCJpYXQiOjE3OTU0MzQ1MjUsImV4cCI6MjExMTAxMDUyNX0.4Xm5jUrfVP3rkGBFbWaaY6xJ9BBBONaWzevUNbFg6J4"
	resetPageURL    = "https://taha97kh-vpn5g-app.static.hf.space/reset.html"
)

type authSession struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	Email        string    `json:"email"`
	UserID       string    `json:"user_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type authStore struct {
	mu      sync.Mutex
	session *authSession
}

func newAuthStore() *authStore {
	a := &authStore{}
	a.session, _ = loadSession()
	return a
}

func appDataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "VPN5G"), nil
}

func sessionFilePath() (string, error) {
	dir, err := appDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session.json"), nil
}

func loadSession() (*authSession, error) {
	p, err := sessionFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s authSession
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.AccessToken == "" {
		return nil, errors.New("no session")
	}
	return &s, nil
}

func saveSession(s *authSession) error {
	p, err := sessionFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func clearSessionFile() {
	if p, err := sessionFilePath(); err == nil {
		_ = os.Remove(p)
	}
}

func (a *authStore) signedInEmail() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil || a.session.AccessToken == "" {
		return ""
	}
	return a.session.Email
}

func (a *authStore) accessToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		return ""
	}
	return a.session.AccessToken
}

func (a *authStore) logout() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session = nil
	clearSessionFile()
	subCache.clear()
}

// --- HTTP helpers (never throw; errors are returned) ---

func supabasePost(path string, body any, bearer string) (int, []byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, supabaseURL+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", supabaseAnonKey)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		req.Header.Set("Authorization", "Bearer "+supabaseAnonKey)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

func supabaseGet(path, bearer string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, supabaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("apikey", supabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+bearer)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

// fetchPublicIP asks the get-client-ip Edge Function for the real public IP.
func fetchPublicIP() (string, error) {
	req, err := http.NewRequest(http.MethodGet, supabaseURL+"/functions/v1/get-client-ip", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("apikey", supabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+supabaseAnonKey)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("get-client-ip returned %s", resp.Status)
	}
	var out struct {
		IP string `json:"ip"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.IP), nil
}

// checkRegistration asks the server whether the email or the IP is already
// registered. Returns the raw body on 2xx, "" on any failure (fail CLOSED).
func checkRegistration(email, ip string) (string, error) {
	code, body, err := supabasePost("/rest/v1/rpc/check_registration",
		map[string]string{"p_email": email, "p_ip": ip}, "")
	if err != nil {
		return "", err
	}
	if code < 200 || code > 299 {
		return "", fmt.Errorf("check_registration returned %d", code)
	}
	return string(body), nil
}

// registerAccount records the (email, ip) pair after a successful signup.
// Best effort: a failure never invalidates the real session.
func registerAccount(email, ip string) bool {
	code, _, err := supabasePost("/rest/v1/rpc/register_account",
		map[string]string{"p_email": email, "p_ip": ip}, "")
	return err == nil && code >= 200 && code <= 299
}

// setOwnUserID links the registrations row to the Supabase user id.
// Best effort; the row is still usable without it.
func (a *authStore) setOwnUserID() bool {
	tok := a.accessToken()
	if tok == "" {
		return false
	}
	code, _, err := supabasePost("/rest/v1/rpc/set_own_user_id", map[string]any{}, tok)
	return err == nil && code >= 200 && code <= 299
}

func parseAuthSession(body []byte, email string) (*authSession, error) {
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		User         struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("no session returned")
	}
	s := &authSession{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		TokenType:    out.TokenType,
		ExpiresIn:    out.ExpiresIn,
		Email:        out.User.Email,
		UserID:       out.User.ID,
		ExpiresAt:    time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}
	if s.Email == "" {
		s.Email = email
	}
	return s, nil
}

func validEmailPassword(email, password string) error {
	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("ایمیل معتبر وارد کنید")
	}
	if len(password) < 6 {
		return errors.New("رمز عبور باید حداقل ۶ کاراکتر باشد")
	}
	return nil
}

// signup registers with email + password. One account per IP + email.
// Fails CLOSED when the IP cannot be fetched or the registration cannot be
// checked — no signup is attempted on an unchecked registration.
func (a *authStore) signup(email, password string) error {
	email = strings.TrimSpace(email)
	if err := validEmailPassword(email, password); err != nil {
		return err
	}
	ip, err := fetchPublicIP()
	if err != nil || ip == "" {
		return errors.New("خطای شبکه — دوباره تلاش کنید")
	}
	body, err := checkRegistration(email, ip)
	if err != nil || body == "" {
		return errors.New("خطای شبکه — دوباره تلاش کنید")
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "email") && strings.Contains(lower, "taken") {
		return errors.New("این ایمیل قبلاً ثبت شده است؛ وارد شوید")
	}
	if strings.Contains(lower, "ip") && strings.Contains(lower, "taken") {
		return errors.New("با این اینترنت قبلاً یک حساب ساخته شده است؛ هر اینترنت فقط یک حساب")
	}
	code, respBody, err := supabasePost("/auth/v1/signup",
		map[string]string{"email": email, "password": password}, "")
	if err != nil {
		return errors.New("خطای شبکه — دوباره تلاش کنید")
	}
	if code == 422 || strings.Contains(strings.ToLower(string(respBody)), "already registered") {
		return errors.New("این ایمیل قبلاً ثبت شده است؛ وارد شوید")
	}
	if code == 429 {
		return errors.New("درخواست زیاد است — چند دقیقه دیگر تلاش کنید")
	}
	if code < 200 || code > 299 {
		return fmt.Errorf("ثبت‌نام ناموفق بود (%d)", code)
	}
	s, err := parseAuthSession(respBody, email)
	if err != nil {
		// Signed up but no session (e.g. confirm email still on):
		// fall back to password login, which returns a session.
		return a.login(email, password)
	}
	if err := saveSession(s); err != nil {
		return err
	}
	a.mu.Lock()
	a.session = s
	a.mu.Unlock()
	go a.setOwnUserID()
	go func() { registerAccount(email, ip) }()
	return nil
}

// login signs in with email + password.
func (a *authStore) login(email, password string) error {
	email = strings.TrimSpace(email)
	if err := validEmailPassword(email, password); err != nil {
		return err
	}
	code, body, err := supabasePost("/auth/v1/token?grant_type=password",
		map[string]string{"email": email, "password": password}, "")
	if err != nil {
		return errors.New("خطای شبکه — دوباره تلاش کنید")
	}
	if code == 400 || code == 401 {
		return errors.New("ایمیل یا رمز عبور اشتباه است")
	}
	if code == 429 {
		return errors.New("درخواست زیاد است — چند دقیقه دیگر تلاش کنید")
	}
	if code < 200 || code > 299 {
		return fmt.Errorf("ورود ناموفق بود (%d)", code)
	}
	s, err := parseAuthSession(body, email)
	if err != nil {
		return err
	}
	if err := saveSession(s); err != nil {
		return err
	}
	a.mu.Lock()
	a.session = s
	a.mu.Unlock()
	go a.setOwnUserID()
	go a.refreshSubscription()
	return nil
}

// sendRecoveryLink emails the password-reset LINK (not a code).
func sendRecoveryLink(email string) error {
	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("ایمیل معتبر وارد کنید")
	}
	code, _, err := supabasePost("/auth/v1/recover",
		map[string]string{"email": email, "redirect_to": resetPageURL}, "")
	if err != nil {
		return errors.New("خطای شبکه — دوباره تلاش کنید")
	}
	if code == 429 {
		return errors.New("درخواست زیاد است — چند دقیقه دیگر تلاش کنید")
	}
	if code < 200 || code > 299 {
		return fmt.Errorf("ارسال لینک ناموفق بود (%d)", code)
	}
	return nil
}

// --- Subscription (server truth + local cache) ---

type subscriptionInfo struct {
	Status    string    `json:"status"` // "active" | "gift" | "none"
	Plan      string    `json:"plan"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s subscriptionInfo) active() bool {
	if s.Status != "active" && s.Status != "gift" {
		return false
	}
	return s.ExpiresAt.IsZero() || time.Now().Before(s.ExpiresAt)
}

func (s subscriptionInfo) isGift() bool { return s.Status == "gift" && s.active() }

func (s subscriptionInfo) remainingDays() int {
	if s.ExpiresAt.IsZero() {
		return 0
	}
	d := int(time.Until(s.ExpiresAt).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}

// subCache persists the last known subscription locally (display hint only;
// the server is the authority — see refreshSubscription).
type subCacheT struct {
	mu  sync.Mutex
	sub subscriptionInfo
}

var subCache = &subCacheT{}

func subFilePath() (string, error) {
	dir, err := appDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "subscription.json"), nil
}

func (c *subCacheT) load() {
	p, err := subFilePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var s subscriptionInfo
	if err := json.Unmarshal(data, &s); err == nil {
		c.mu.Lock()
		c.sub = s
		c.mu.Unlock()
	}
}

func (c *subCacheT) save() {
	c.mu.Lock()
	s := c.sub
	c.mu.Unlock()
	p, err := subFilePath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	data, _ := json.Marshal(s)
	_ = os.WriteFile(p, data, 0o600)
}

func (c *subCacheT) get() subscriptionInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sub
}

func (c *subCacheT) set(s subscriptionInfo) {
	c.mu.Lock()
	// Later expiry wins; a cached subscription is never demoted by an
	// empty/failed fetch (the caller only sets real results).
	c.sub = s
	c.mu.Unlock()
	c.save()
}

func (c *subCacheT) clear() {
	c.mu.Lock()
	c.sub = subscriptionInfo{}
	c.mu.Unlock()
	if p, err := subFilePath(); err == nil {
		_ = os.Remove(p)
	}
}

func parseExpiresAt(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

// fetchServerSubscription asks the bot's RPC for the subscription truth
// (bot-activated plans AND redeemed gifts, recorded centrally).
func (a *authStore) fetchServerSubscription(email string) (subscriptionInfo, bool) {
	code, body, err := supabasePost("/rest/v1/rpc/get_subscription_status",
		map[string]string{"p_email": email}, "")
	if err != nil || code < 200 || code > 299 {
		return subscriptionInfo{}, false
	}
	var out struct {
		Status    string `json:"status"`
		Plan      string `json:"plan"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return subscriptionInfo{}, false
	}
	st := strings.ToLower(strings.TrimSpace(out.Status))
	if st == "none" || st == "inactive" || st == "cancelled" || st == "" {
		return subscriptionInfo{Status: "none"}, true
	}
	return subscriptionInfo{
		Status:    "active",
		Plan:      out.Plan,
		ExpiresAt: parseExpiresAt(out.ExpiresAt),
	}, true
}

// fetchOwnRow reads the user's own registrations row via RLS (own access
// token). The row carries subscription_status / plan / expiry set by the
// bot admin.
func (a *authStore) fetchOwnRow() (subscriptionInfo, bool) {
	a.mu.Lock()
	uid := ""
	if a.session != nil {
		uid = a.session.UserID
	}
	tok := ""
	if a.session != nil {
		tok = a.session.AccessToken
	}
	a.mu.Unlock()
	if uid == "" || tok == "" {
		return subscriptionInfo{}, false
	}
	path := "/rest/v1/vpn5g_registrations?select=subscription_status,subscription_expires_at,subscription_plan&user_id=eq." + uid
	code, body, err := supabaseGet(path, tok)
	if err != nil || code < 200 || code > 299 {
		return subscriptionInfo{}, false
	}
	var rows []struct {
		Status    string `json:"subscription_status"`
		ExpiresAt string `json:"subscription_expires_at"`
		Plan      string `json:"subscription_plan"`
	}
	if err := json.Unmarshal(body, &rows); err != nil || len(rows) == 0 {
		return subscriptionInfo{}, false
	}
	row := rows[0]
	st := strings.ToLower(strings.TrimSpace(row.Status))
	if st != "active" {
		return subscriptionInfo{Status: "none"}, true
	}
	return subscriptionInfo{
		Status:    "active",
		Plan:      row.Plan,
		ExpiresAt: parseExpiresAt(row.ExpiresAt),
	}, true
}

// refreshSubscription merges server truth into the local cache. The server
// is the authority; a failed fetch keeps the cache (never a false denial).
func (a *authStore) refreshSubscription() {
	email := a.signedInEmail()
	if email == "" {
		return
	}
	if sub, ok := a.fetchServerSubscription(email); ok {
		if sub.active() {
			subCache.set(sub)
			return
		}
		if sub.Status == "none" {
			// Server says none — but a locally redeemed gift/subscription
			// with a later expiry still wins (merge rule, Android parity).
			cur := subCache.get()
			if !cur.active() {
				subCache.set(subscriptionInfo{Status: "none"})
			}
			return
		}
	}
	if sub, ok := a.fetchOwnRow(); ok && sub.active() {
		subCache.set(sub)
	}
}

// --- Code validation + redemption (best effort, never throw) ---

// validateAndRedeemCode validates + redeems a code SERVER-side.
// Returns (status, plan, expiresAt, available). When the RPC is missing
// or unreachable, available=false and the caller falls back to the
// published config list.
func (a *authStore) validateAndRedeemCode(code, email string) (string, string, time.Time, bool) {
	rcode, body, err := supabasePost("/rest/v1/rpc/validate_and_redeem_code",
		map[string]string{"p_code": strings.ToUpper(strings.TrimSpace(code)), "p_email": strings.TrimSpace(email)}, "")
	if err != nil || rcode < 200 || rcode > 299 {
		return "", "", time.Time{}, false
	}
	var out struct {
		Status    string `json:"status"`
		Plan      string `json:"plan"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", time.Time{}, false
	}
	return strings.ToLower(strings.TrimSpace(out.Status)), out.Plan, parseExpiresAt(out.ExpiresAt), true
}

// recordCodeRedemption stores a redemption row server-side (the bot uses
// the count to enforce the people-limit when it republishes). Best effort.
func (a *authStore) recordCodeRedemption(code, email string) bool {
	rcode, body, err := supabasePost("/rest/v1/rpc/record_code_redemption",
		map[string]string{"p_code": strings.ToUpper(strings.TrimSpace(code)), "p_email": strings.TrimSpace(email)}, "")
	if err != nil || rcode < 200 || rcode > 299 {
		return false
	}
	return strings.TrimSpace(string(body)) == "true"
}

// recordAdView / recordAdClick report a fully-viewed ad / a tap
// server-side. Best effort: a failure never stops the countdown or the
// connect. The server timestamps the event (never the phone clock).
func (a *authStore) recordAdView(adID, email string) bool {
	rcode, _, err := supabasePost("/rest/v1/rpc/record_ad_view",
		map[string]string{"p_ad_id": adID, "p_email": email}, "")
	return err == nil && rcode >= 200 && rcode <= 299
}

func (a *authStore) recordAdClick(adID, email string) bool {
	rcode, _, err := supabasePost("/rest/v1/rpc/record_ad_click",
		map[string]string{"p_ad_id": adID, "p_email": email}, "")
	return err == nil && rcode >= 200 && rcode <= 299
}
