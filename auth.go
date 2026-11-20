package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Supabase Auth (same project as the Android app). The anon key below is a
// public client key by design — it is safe to ship, and the Android app
// ships the same one. NEVER put a service_role key or DB password here.
const (
	supabaseURL     = "https://jsqvjpmqfmgfgzcycngs.supabase.co"
	supabaseAnonKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6ImpzcXZqcG1xZm1nZmd6Y3ljbmdzIiwicm9sZSI6ImFub24iLCJpYXQiOjE3OTUxNzg4ODgsImV4cCI6MjExMDc1NDg4OH0.ZfRnA0GznbVE9zVZ8ndKSq04W-9AB4RJTKErXkxB7ZA"
)

type authSession struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	Email        string    `json:"email"`
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

func sessionFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "VPN5G", "session.json"), nil
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
	if s.AccessToken == "" || time.Now().After(s.ExpiresAt) {
		return nil, errors.New("session expired")
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
	if a.session == nil {
		return ""
	}
	if time.Now().After(a.session.ExpiresAt) {
		a.session = nil
		clearSessionFile()
		return ""
	}
	return a.session.Email
}

func (a *authStore) logout() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session = nil
	clearSessionFile()
}

func supabaseRequest(path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, supabaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", supabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+supabaseAnonKey)
	client := &http.Client{Timeout: 20 * time.Second}
	return client.Do(req)
}

// sendOTP asks Supabase to email a 6-digit code.
func sendOTP(email string) error {
	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("enter a valid email address")
	}
	resp, err := supabaseRequest("/auth/v1/otp", map[string]any{"email": email})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("supabase returned %s", resp.Status)
	}
	return nil
}

// verifyOTP exchanges the 6-digit code for a session and stores it locally.
func (a *authStore) verifyOTP(email, token string) error {
	email = strings.TrimSpace(email)
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("enter the 6-digit code")
	}
	resp, err := supabaseRequest("/auth/v1/verify", map[string]any{
		"email": email,
		"token": token,
		"type":  "email",
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wrong or expired code (%s)", resp.Status)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		User         struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.AccessToken == "" {
		return errors.New("no session returned")
	}
	s := &authSession{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		TokenType:    out.TokenType,
		ExpiresIn:    out.ExpiresIn,
		Email:        out.User.Email,
		ExpiresAt:    time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}
	if s.Email == "" {
		s.Email = email
	}
	if err := saveSession(s); err != nil {
		return err
	}
	a.mu.Lock()
	a.session = s
	a.mu.Unlock()
	return nil
}
