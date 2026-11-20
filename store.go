package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	liveConfigsURL = "https://taha97kh-vpn5g-app.static.hf.space/live_configs.json"
	subTxtURL      = "https://taha97kh-vpn5g-app.static.hf.space/sub.txt"
	freeV2rayURL   = "https://raw.githubusercontent.com/0xRadikal/Free-v2ray-Configs/main/verified/configs.txt"
	barryFarURL    = "https://raw.githubusercontent.com/barry-far/V2ray-Config/main/All_Configs_Sub.txt"
	appConfigURL   = "https://taha97kh-vpn5g-app.static.hf.space/app_config.json"

	// Per GitHub source, ping-test at most this many sampled lines per refresh.
	maxSamplePerGitHub = 300
	pingTimeout        = 3 * time.Second
	pingWorkers        = 32
	refreshInterval    = 5 * time.Minute
)

var httpClient = &http.Client{Timeout: 20 * time.Second}

// ServerEntry is one ping-tested, showable server.
type ServerEntry struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	Country  string `json:"country"`
	Name     string `json:"name"`
	URI      string `json:"uri"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	PingMs   int64  `json:"ping_ms"`
	Free     bool   `json:"free"`
}

type serverStore struct {
	mu         sync.RWMutex
	servers    []*ServerEntry
	updatedAt  time.Time
	refreshing bool
}

func newServerStore() *serverStore { return &serverStore{} }

func (s *serverStore) getServers() []*ServerEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*ServerEntry, len(s.servers))
	copy(out, s.servers)
	return out
}

func (s *serverStore) getByID(id string) *ServerEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.servers {
		if e.ID == id {
			return e
		}
	}
	return nil
}

func (s *serverStore) status() (count int, updatedAt time.Time, refreshing bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.servers), s.updatedAt, s.refreshing
}

func (s *serverStore) refreshLoop() {
	s.refresh()
	for range time.Tick(refreshInterval) {
		s.refresh()
	}
}

func (s *serverStore) refresh() {
	s.mu.Lock()
	s.refreshing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.refreshing = false
		s.mu.Unlock()
	}()

	seen := map[string]bool{}
	var all []*ServerEntry
	add := func(e *ServerEntry) {
		if e == nil || e.URI == "" || seen[e.URI] {
			return
		}
		seen[e.URI] = true
		all = append(all, e)
	}

	// Source 1: live_configs.json — always fully ping-tested.
	for _, e := range fetchLiveConfigs() {
		add(e)
	}

	// Sources 2-4 + any extra sources from app_config.json.
	urls := []string{subTxtURL, freeV2rayURL, barryFarURL}
	urls = append(urls, fetchExtraSources()...)
	for _, u := range urls {
		maxSample := maxSamplePerGitHub
		if u == subTxtURL {
			maxSample = 0 // our own subscription: no cap
		}
		for _, e := range fetchSubscription(u, maxSample) {
			add(e)
		}
	}

	// HARD RULE: configs with no ping are NEVER shown.
	pinged := pingEntries(all)
	sort.Slice(pinged, func(i, j int) bool { return pinged[i].PingMs < pinged[j].PingMs })
	for i, e := range pinged {
		e.Free = i < freeLimit
	}

	s.mu.Lock()
	s.servers = pinged
	s.updatedAt = time.Now()
	s.mu.Unlock()
}

// liveEntry mirrors one object of live_configs.json.
// Observed schema (2026-11-20): protocol, country, ping, raw_ping, uri,
// name, host, port. Only uri/country/name/host/port are used; ping is
// re-measured locally.
type liveEntry struct {
	Protocol string `json:"protocol"`
	Country  string `json:"country"`
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

func fetchLiveConfigs() []*ServerEntry {
	resp, err := httpClient.Get(liveConfigsURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var entries []liveEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil
	}
	var out []*ServerEntry
	for _, le := range entries {
		if le.URI == "" || le.Host == "" || le.Port == 0 {
			continue
		}
		out = append(out, &ServerEntry{
			ID:       entryID(le.URI),
			Protocol: strings.ToLower(strings.TrimSpace(le.Protocol)),
			Country:  strings.TrimSpace(le.Country),
			Name:     strings.TrimSpace(le.Name),
			URI:      strings.TrimSpace(le.URI),
			Host:     le.Host,
			Port:     le.Port,
		})
	}
	return out
}

// fetchExtraSources reads app_config.json's `config_sources` field when
// present. The file currently 404s; that is treated as "no extra sources".
func fetchExtraSources() []string {
	resp, err := httpClient.Get(appConfigURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var cfg struct {
		ConfigSources []string `json:"config_sources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil
	}
	return cfg.ConfigSources
}

// fetchSubscription downloads a subscription file (base64-encoded or plain
// link lines), samples at most maxSample lines when maxSample > 0, and
// parses the links.
func fetchSubscription(url string, maxSample int) []*ServerEntry {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil
	}
	// Try base64 first: the whole file may be one base64 blob.
	if decoded, err := decodeB64Flexible(text); err == nil {
		if s := strings.TrimSpace(string(decoded)); strings.Contains(s, "://") {
			text = s
		}
	}
	lines := strings.Split(text, "\n")
	var links []string
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		// A line may itself be base64 of a single link.
		if !strings.Contains(ln, "://") {
			if decoded, err := decodeB64Flexible(ln); err == nil {
				if s := strings.TrimSpace(string(decoded)); strings.Contains(s, "://") {
					ln = s
				}
			}
		}
		if strings.Contains(ln, "://") {
			links = append(links, ln)
		}
	}
	if maxSample > 0 && len(links) > maxSample {
		links = strideSample(links, maxSample)
	}
	var out []*ServerEntry
	for _, ln := range links {
		if e, err := parseLink(ln); err == nil && e != nil {
			out = append(out, e)
		}
	}
	return out
}

// strideSample picks n evenly-spaced items so the sample covers the file.
func strideSample(in []string, n int) []string {
	out := make([]string, 0, n)
	step := float64(len(in)) / float64(n)
	for i := 0; i < n; i++ {
		out = append(out, in[int(float64(i)*step)])
	}
	return out
}

func entryID(uri string) string {
	sum := sha1.Sum([]byte(uri))
	return hex.EncodeToString(sum[:])[:16]
}

// pingEntries TCP-connects to every host:port and returns only the ones
// that answered, with the measured latency. Configs with no ping are
// dropped here and never reach the UI.
func pingEntries(in []*ServerEntry) []*ServerEntry {
	jobs := make(chan *ServerEntry)
	results := make(chan *ServerEntry)
	var wg sync.WaitGroup
	for i := 0; i < pingWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				start := time.Now()
				conn, err := net.DialTimeout("tcp", net.JoinHostPort(e.Host, itoa(e.Port)), pingTimeout)
				if err != nil {
					continue
				}
				_ = conn.Close()
				e.PingMs = time.Since(start).Milliseconds()
				results <- e
			}
		}()
	}
	go func() {
		for _, e := range in {
			if e.Host == "" || e.Port == 0 {
				continue
			}
			jobs <- e
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	var out []*ServerEntry
	for e := range results {
		out = append(out, e)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// decodeB64Flexible decodes standard, raw, URL-safe, or whitespace-padded
// base64.
func decodeB64Flexible(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, base64.CorruptInputError(0)
}
