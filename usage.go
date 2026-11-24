package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// usage.go — per-subscription traffic accounting (Model B: volume OR time,
// whichever comes first).
//
// The cap comes ONLY from the server-published plan config: the plan's
// "gb" field, in gigabytes (e.g. gb_20 -> 20). A plan without "gb" (the
// sub_* time plans) has NO volume cap — its volume is unlimited. A cap is
// never invented locally.
//
// Usage is measured ONLY from the real local tunnel: Xray's cumulative
// outbound downlink+uplink counters (see queryXrayStats in stats.go),
// accumulated while the tunnel runs. Nothing is estimated, sampled from
// the network, or faked: when the tunnel is down, no usage accrues, and
// when the stats query fails the counters simply don't move.
//
// The ledger is keyed by the subscription period (plan + expires_at): a
// new code or a renewal starts a fresh period with 0 used bytes. It is
// persisted next to the other local state (usage.json in the app data
// dir) so an app restart never loses the count — Xray's own counters DO
// reset on restart, so the ledger keeps its own baseline and re-bases
// whenever a counter moves backwards.
//
// 1 GB = 1024^3 bytes (GiB), the same unit VPN panels use for data
// limits. The bot publishes the cap in GB; the device enforces it.

// bytesPerGB is one gigabyte in bytes (1024^3).
const bytesPerGB = int64(1024 * 1024 * 1024)

type usageLedger struct {
	Plan      string    `json:"plan"`
	ExpiresAt time.Time `json:"expires_at"`
	UsedBytes int64     `json:"used_bytes"`
	LastDown  int64     `json:"last_down_total"`
	LastUp    int64     `json:"last_up_total"`
	UpdatedAt time.Time `json:"updated_at"`
}

type usageStore struct {
	mu       sync.Mutex
	ledger   usageLedger
	lastSave time.Time
}

var usage = &usageStore{}

// volumeInfo is the read model for one subscription's volume state.
type volumeInfo struct {
	Limited        bool
	CapGB          float64
	UsedBytes      int64
	RemainingBytes int64
	Percent        float64 // 0..100; 0 when unlimited
	Exceeded       bool
}

func usageFilePath() (string, error) {
	dir, err := appDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "usage.json"), nil
}

func (u *usageStore) load() {
	p, err := usageFilePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var l usageLedger
	if err := json.Unmarshal(data, &l); err == nil {
		u.mu.Lock()
		u.ledger = l
		u.mu.Unlock()
	}
}

func (u *usageStore) save() {
	u.mu.Lock()
	l := u.ledger
	u.mu.Unlock()
	p, err := usageFilePath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	data, _ := json.Marshal(l)
	if err := os.WriteFile(p, data, 0o600); err == nil {
		u.mu.Lock()
		u.lastSave = time.Now()
		u.mu.Unlock()
	}
}

// saveThrottled persists the ledger at most once every 15 seconds; the
// in-memory count is always exact, the file just lags a little behind.
// Callers that need a durable count right now (disconnect) use save().
func (u *usageStore) saveThrottled() {
	u.mu.Lock()
	due := time.Since(u.lastSave) >= 15*time.Second
	u.mu.Unlock()
	if due {
		u.save()
	}
}

// volumeCapGB returns the traffic cap in gigabytes for this subscription's
// plan, from the server-published plan config. 0 = unlimited.
func volumeCapGB(sub subscriptionInfo) float64 {
	if sub.Plan == "" {
		return 0
	}
	return remoteCfg.planCapGB(sub.Plan)
}

// noteSample folds one Xray stats tick into the ledger. down/up are the
// cumulative outbound counters from queryXrayStats. Only volume-capped
// subscriptions are tracked; time-only plans and the free/gift tiers
// (no "gb" on their plan) accrue nothing and are never capped.
func (u *usageStore) noteSample(sub subscriptionInfo, down, up int64) {
	capGB := volumeCapGB(sub)
	if capGB <= 0 {
		return
	}
	now := time.Now()
	u.mu.Lock()
	if u.ledger.Plan != sub.Plan || !u.ledger.ExpiresAt.Equal(sub.ExpiresAt) {
		// New subscription period (new code / renewal / different plan):
		// fresh count, baseline at the current counters.
		u.ledger = usageLedger{
			Plan:      sub.Plan,
			ExpiresAt: sub.ExpiresAt,
			UsedBytes: 0,
			LastDown:  down,
			LastUp:    up,
			UpdatedAt: now,
		}
		u.mu.Unlock()
		u.save()
		return
	}
	// Counters only move forward; a backwards step means Xray restarted
	// and its counters reset — re-base that direction, count 0 for it.
	deltaDown := down - u.ledger.LastDown
	if deltaDown < 0 {
		deltaDown = 0
	}
	deltaUp := up - u.ledger.LastUp
	if deltaUp < 0 {
		deltaUp = 0
	}
	before := u.ledger.UsedBytes
	u.ledger.UsedBytes += deltaDown + deltaUp
	u.ledger.LastDown = down
	u.ledger.LastUp = up
	u.ledger.UpdatedAt = now
	capBytes := int64(capGB * float64(bytesPerGB))
	crossed := before < capBytes && u.ledger.UsedBytes >= capBytes
	u.mu.Unlock()
	if crossed {
		log.Printf("volume cap reached: plan %s used %d bytes of %d (%.1f GB)",
			sub.Plan, u.ledger.UsedBytes, capBytes, capGB)
	}
	u.saveThrottled()
}

// snapshot returns the volume state for this subscription. When the
// ledger belongs to a different period, the current period has used 0.
func (u *usageStore) snapshot(sub subscriptionInfo) volumeInfo {
	capGB := volumeCapGB(sub)
	if capGB <= 0 {
		return volumeInfo{Limited: false, CapGB: 0}
	}
	capBytes := int64(capGB * float64(bytesPerGB))
	u.mu.Lock()
	used := int64(0)
	if u.ledger.Plan == sub.Plan && u.ledger.ExpiresAt.Equal(sub.ExpiresAt) {
		used = u.ledger.UsedBytes
	}
	u.mu.Unlock()
	remaining := capBytes - used
	if remaining < 0 {
		remaining = 0
	}
	pct := float64(used) / float64(capBytes) * 100
	if pct > 100 {
		pct = 100
	}
	return volumeInfo{
		Limited:        true,
		CapGB:          capGB,
		UsedBytes:      used,
		RemainingBytes: remaining,
		Percent:        pct,
		Exceeded:       used >= capBytes,
	}
}

// exceeded reports whether this subscription has used up its volume cap
// (Model B). Unlimited plans never exceed.
func (u *usageStore) exceeded(sub subscriptionInfo) bool {
	return u.snapshot(sub).Exceeded
}

// volumeWatchdog disconnects the tunnel as soon as a volume-capped
// subscription runs out mid-session. Time expiry is already enforced by
// the subscription itself; this covers the volume half of Model B.
func volumeWatchdog(conn *connection) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		if conn.isActive() && usage.exceeded(subCache.get()) {
			sub := subCache.get()
			log.Printf("volume cap reached for plan %s — disconnecting", sub.Plan)
			conn.disconnect()
		}
	}
}
