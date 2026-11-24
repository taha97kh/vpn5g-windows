package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// session.go — the VPN session: xray child process + system proxy +
// tunnel verification + failover + session timer.
//
// Subscribers (and the gift tier while it lasts) have NO 30-minute cutoff
// and the full server pool; free users keep the 30-minute timer and the
// 20 fastest servers. A connect is only reported as connected after the
// tunnel is VERIFIED: the exit IP seen through the proxy must differ from
// the baseline IP (or, when no baseline could be fetched, the proxy must
// at least return a valid IP). On a failed verification the app fails over
// to the next server automatically (max 3 attempts, different country
// first) — it never reports a dead tunnel as connected.

type connection struct {
	mu        sync.Mutex
	store     *serverStore
	auth      *authStore
	xray      *xrayRunner
	active    bool
	server    *ServerEntry
	startedAt time.Time
	deadline  time.Time
	unlimited bool
	stopTimer chan struct{}
}

func newConnection(s *serverStore, a *authStore) *connection {
	return &connection{store: s, auth: a, xray: &xrayRunner{}}
}

// allowedServers returns the servers this user may use: free = 20 fastest,
// gift = 50 fastest, subscriber = the whole pool.
func (c *connection) allowedServers() []*ServerEntry {
	all := c.store.getServers()
	sub := subCache.get()
	if sub.active() && !sub.isGift() {
		return all
	}
	if sub.isGift() {
		if len(all) > 50 {
			return all[:50]
		}
		return all
	}
	var free []*ServerEntry
	for _, e := range all {
		if e.Free {
			free = append(free, e)
		}
	}
	return free
}

func (c *connection) isAllowed(id string) bool {
	for _, e := range c.allowedServers() {
		if e.ID == id {
			return true
		}
	}
	return false
}

// fetchIP fetches the public IP, optionally through the local proxy.
func fetchIPVia(proxyURL string, timeout time.Duration) string {
	client := &http.Client{Timeout: timeout}
	if proxyURL != "" {
		tr := &http.Transport{Proxy: mustProxyURL(proxyURL)}
		client.Transport = tr
	}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		resp2, err2 := client.Get("https://api.ip.sb/ip")
		if err2 != nil {
			return ""
		}
		defer resp2.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp2.Body, 64))
		return strings.TrimSpace(string(b))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return strings.TrimSpace(string(b))
}

func mustProxyURL(raw string) func(*http.Request) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return func(*http.Request) (*url.URL, error) { return u, nil }
}

// verifyTunnel checks the tunnel really works: the exit IP through the
// proxy must differ from the baseline, or (no baseline) be a valid IP.
func verifyTunnel(baseline string) bool {
	seen := fetchIPVia("http://127.0.0.1:10809", 10*time.Second)
	if seen == "" {
		return false
	}
	if baseline == "" {
		return true
	}
	return seen != baseline
}

// connectURI connects to a server built from a raw share link (used by the
// Chrome extension helper contract: the extension picked the server
// itself). The entry is verified the same way as a store server; on a
// failed verification it fails over to the fastest allowed store servers.
func (c *connection) connectURI(e *ServerEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		c.disconnectLocked()
	}
	sub := subCache.get()
	baseline := fetchIPVia("", 5*time.Second)
	candidates := []*ServerEntry{e}
	seen := map[string]bool{e.ID: true}
	for _, s := range c.allowedServers() {
		if len(candidates) >= 3 {
			break
		}
		if seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		candidates = append(candidates, s)
	}
	var lastErr error
	for _, cand := range candidates {
		if err := c.tryConnect(cand, sub); err != nil {
			lastErr = err
			log.Printf("connect to %s failed: %v", cand.Name, err)
			continue
		}
		if verifyTunnel(baseline) {
			log.Printf("connected to %s (%s, %dms) — tunnel verified", cand.Name, cand.Country, cand.PingMs)
			return nil
		}
		log.Printf("tunnel verification failed for %s, trying next", cand.Name)
		c.disconnectLocked()
		lastErr = errors.New("tunnel verification failed")
	}
	if lastErr == nil {
		lastErr = errors.New("could not connect")
	}
	return lastErr
}

// connect starts the tunnel to the given server (or, when id is "", the
// fastest allowed server). userInitiated marks a real user tap — only
// those count for the forced-ad rule.
func (c *connection) connect(id string, userInitiated bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		c.disconnectLocked()
	}
	// NOTE: connect attempts are counted once, in the /api/connect handler
	// (adGate.noteUserConnectAttempt) — never here, or a tap would count
	// twice and the forced-ad rule would fire early.
	sub := subCache.get()

	baseline := fetchIPVia("", 5*time.Second)

	var first *ServerEntry
	if id != "" {
		first = c.store.getByID(id)
		if first == nil {
			return errors.New("server not found")
		}
		if !c.isAllowed(id) {
			return errors.New("vip")
		}
	} else {
		allowed := c.allowedServers()
		if len(allowed) == 0 {
			return errors.New("no server available")
		}
		first = allowed[0]
	}

	// Candidate order: the chosen server first, then the fastest allowed
	// servers (different country first), up to 3 attempts total.
	candidates := []*ServerEntry{first}
	seen := map[string]bool{first.ID: true}
	for _, e := range c.allowedServers() {
		if len(candidates) >= 3 {
			break
		}
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		if e.Country != first.Country {
			candidates = append(candidates, e)
		}
	}
	for _, e := range c.allowedServers() {
		if len(candidates) >= 3 {
			break
		}
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		candidates = append(candidates, e)
	}

	var lastErr error
	for _, e := range candidates {
		if err := c.tryConnect(e, sub); err != nil {
			lastErr = err
			log.Printf("connect to %s failed: %v", e.Name, err)
			continue
		}
		if verifyTunnel(baseline) {
			log.Printf("connected to %s (%s, %dms) — tunnel verified", e.Name, e.Country, e.PingMs)
			return nil
		}
		log.Printf("tunnel verification failed for %s, trying next", e.Name)
		c.disconnectLocked()
		lastErr = errors.New("tunnel verification failed")
	}
	if lastErr == nil {
		lastErr = errors.New("could not connect")
	}
	return lastErr
}

func (c *connection) tryConnect(e *ServerEntry, sub subscriptionInfo) error {
	outbound, err := buildOutbound(e)
	if err != nil {
		return err
	}
	if err := c.xray.start(outbound); err != nil {
		return err
	}
	if err := setSystemProxy(true); err != nil {
		c.xray.stop()
		return err
	}
	c.active = true
	c.server = e
	c.startedAt = time.Now()
	c.unlimited = sub.active()
	if c.unlimited {
		c.deadline = time.Time{}
	} else {
		c.deadline = c.startedAt.Add(sessionMinutes * time.Minute)
		c.stopTimer = make(chan struct{})
		go c.watchdog(c.stopTimer, c.deadline)
	}
	speeds.start()
	return nil
}

func (c *connection) watchdog(stop chan struct{}, deadline time.Time) {
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-t.C:
		log.Println("30-minute session expired, auto-disconnecting")
		c.disconnect()
	case <-stop:
	}
}

func (c *connection) disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnectLocked()
}

func (c *connection) disconnectLocked() {
	if c.stopTimer != nil {
		close(c.stopTimer)
		c.stopTimer = nil
	}
	speeds.stop()
	c.xray.stop()
	_ = setSystemProxy(false)
	c.active = false
	c.server = nil
	c.unlimited = false
}

// status returns the JSON-friendly connection state.
func (c *connection) status() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	sub := subCache.get()
	st := map[string]any{
		"connected":       c.active,
		"session_minutes": sessionMinutes,
		"unlimited":       c.unlimited,
		"subscriber":      sub.active() && !sub.isGift(),
		"gift":            sub.isGift(),
		"plan":            sub.Plan,
		"remaining_days":  sub.remainingDays(),
	}
	if c.active {
		st["server_id"] = c.server.ID
		st["server_name"] = c.server.Name
		st["country"] = c.server.Country
		st["protocol"] = c.server.Protocol
		st["ping_ms"] = c.server.PingMs
		st["started_at"] = c.startedAt.Format(time.RFC3339)
		if !c.unlimited {
			remaining := int(time.Until(c.deadline).Seconds())
			if remaining < 0 {
				remaining = 0
			}
			st["remaining_seconds"] = remaining
		}
	}
	sp := speeds.snapshot()
	st["speeds_available"] = sp.Available
	st["down_bps"] = sp.DownBps
	st["up_bps"] = sp.UpBps
	return st
}
