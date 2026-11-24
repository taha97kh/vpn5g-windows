package main

import (
	"testing"
	"time"
)

// usage_test.go — Model B volume accounting: cap from the published plan,
// usage from the (simulated) Xray cumulative counters, reset on a new
// period, re-base on counter reset, unlimited when the plan has no "gb".

func testVolumeSetup(t *testing.T) {
	t.Helper()
	usage = &usageStore{}
	remoteCfg.mu.Lock()
	oldCfg, oldOk := remoteCfg.cfg, remoteCfg.ok
	remoteCfg.cfg = appConfigRemote{Plans: []planRemote{
		{ID: "gb_20", Days: 30, PriceToman: 50000, NameFa: "بسته ۲۰ گیگابایت", GB: 20},
		{ID: "sub_1", Days: 30, PriceToman: 50000, NameFa: "اشتراک ۱ ماهه"},
	}}
	remoteCfg.ok = true
	remoteCfg.mu.Unlock()
	t.Cleanup(func() {
		remoteCfg.mu.Lock()
		remoteCfg.cfg, remoteCfg.ok = oldCfg, oldOk
		remoteCfg.mu.Unlock()
		usage = &usageStore{}
	})
}

func TestVolumeAccumulatesRealDeltas(t *testing.T) {
	testVolumeSetup(t)
	sub := subscriptionInfo{Status: "active", Plan: "gb_20", ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}

	usage.noteSample(sub, 1000, 500) // first tick: baseline only
	if got := usage.snapshot(sub); got.UsedBytes != 0 {
		t.Fatalf("first tick must only set the baseline, got used=%d", got.UsedBytes)
	}
	usage.noteSample(sub, 3000, 1500)
	got := usage.snapshot(sub)
	if got.UsedBytes != 3000 {
		t.Fatalf("used = %d, want 3000 (2000 down + 1000 up)", got.UsedBytes)
	}
	if !got.Limited || got.CapGB != 20 || got.Exceeded {
		t.Fatalf("snapshot = %+v, want limited cap 20 not exceeded", got)
	}
	if !sub.active() {
		t.Fatalf("subscription must still be active below the cap")
	}
}

func TestVolumeRebasesOnCounterReset(t *testing.T) {
	testVolumeSetup(t)
	sub := subscriptionInfo{Status: "active", Plan: "gb_20", ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	usage.noteSample(sub, 3000, 1500)
	usage.noteSample(sub, 5000, 2500) // used = 3000
	// Xray restarted: counters drop to small values — count 0, re-base.
	usage.noteSample(sub, 100, 50)
	if got := usage.snapshot(sub); got.UsedBytes != 3000 {
		t.Fatalf("after counter reset used = %d, want 3000", got.UsedBytes)
	}
	// And counting resumes from the new baseline.
	usage.noteSample(sub, 1100, 150)
	if got := usage.snapshot(sub); got.UsedBytes != 4100 {
		t.Fatalf("after re-base used = %d, want 4100", got.UsedBytes)
	}
}

func TestVolumeCapEndsSubscription(t *testing.T) {
	testVolumeSetup(t)
	sub := subscriptionInfo{Status: "active", Plan: "gb_20", ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	usage.noteSample(sub, 0, 0)
	// 20 GiB down in one tick.
	usage.noteSample(sub, 20*bytesPerGB, 0)
	got := usage.snapshot(sub)
	if !got.Exceeded || got.Percent != 100 || got.RemainingBytes != 0 {
		t.Fatalf("at cap: %+v", got)
	}
	if sub.active() {
		t.Fatalf("subscription must be INACTIVE once the volume cap is reached, even with days left")
	}
}

func TestVolumeNewPeriodResets(t *testing.T) {
	testVolumeSetup(t)
	exp1 := time.Now().Add(30 * 24 * time.Hour)
	sub1 := subscriptionInfo{Status: "active", Plan: "gb_20", ExpiresAt: exp1}
	usage.noteSample(sub1, 0, 0)
	usage.noteSample(sub1, 1000000, 0)
	if got := usage.snapshot(sub1); got.UsedBytes != 1000000 {
		t.Fatalf("used = %d, want 1000000", got.UsedBytes)
	}
	// Renewal: same plan, later expiry = a fresh period.
	sub2 := subscriptionInfo{Status: "active", Plan: "gb_20", ExpiresAt: exp1.Add(30 * 24 * time.Hour)}
	usage.noteSample(sub2, 1000000, 0)
	if got := usage.snapshot(sub2); got.UsedBytes != 0 {
		t.Fatalf("new period must start at 0, got %d", got.UsedBytes)
	}
	if !sub2.active() {
		t.Fatalf("renewed subscription must be active")
	}
}

func TestVolumeUnlimitedPlan(t *testing.T) {
	testVolumeSetup(t)
	sub := subscriptionInfo{Status: "active", Plan: "sub_1", ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	usage.noteSample(sub, 1000000, 1000000)
	usage.noteSample(sub, 9000000000, 9000000000)
	got := usage.snapshot(sub)
	if got.Limited || got.Exceeded {
		t.Fatalf("time-only plan must be unlimited: %+v", got)
	}
	if !sub.active() {
		t.Fatalf("time-only plan must stay active regardless of traffic")
	}
}
