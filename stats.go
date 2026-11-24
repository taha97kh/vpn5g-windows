package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// stats.go — live tunnel speeds from Xray's stats API.
//
// The Xray config (see writeXrayConfig in parse.go) enables outbound
// traffic stats and exposes the StatsService on 127.0.0.1:10810. While
// connected, this poller runs `xray.exe api statsquery` once a second and
// turns the CUMULATIVE counters into bytes/second rates. Xray's counters
// are cumulative (they are NOT reset per query), so delta math is correct
// here. When the query fails the speeds are UNAVAILABLE — the UI shows
// "—", never a fake zero.

const xrayStatsPort = 10810

type speedSample struct {
	DownBps   int64     `json:"down_bps"`
	UpBps     int64     `json:"up_bps"`
	DownTotal int64     `json:"down_total"`
	UpTotal   int64     `json:"up_total"`
	Available bool      `json:"available"`
	UpdatedAt time.Time `json:"updated_at"`
}

type statsPoller struct {
	mu      sync.Mutex
	current speedSample
	stopCh  chan struct{}
	running bool
}

var speeds = &statsPoller{}

func (p *statsPoller) snapshot() speedSample {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

func (p *statsPoller) start() {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.current = speedSample{}
	p.stopCh = make(chan struct{})
	stopCh := p.stopCh
	p.mu.Unlock()

	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		var lastDown, lastUp int64 = -1, -1
		var lastAt time.Time
		for {
			select {
			case <-stopCh:
				return
			case <-t.C:
				down, up, ok := queryXrayStats()
				now := time.Now()
				p.mu.Lock()
				if ok {
					s := speedSample{DownTotal: down, UpTotal: up, Available: true, UpdatedAt: now}
					if lastDown >= 0 && !lastAt.IsZero() {
						secs := now.Sub(lastAt).Seconds()
						if secs > 0 {
							s.DownBps = int64(float64(down-lastDown) / secs)
							s.UpBps = int64(float64(up-lastUp) / secs)
							if s.DownBps < 0 {
								s.DownBps = 0
							}
							if s.UpBps < 0 {
								s.UpBps = 0
							}
						}
					}
					p.current = s
				} else {
					p.current.Available = false
					p.current.UpdatedAt = now
				}
				p.mu.Unlock()
				if ok {
					// Fold this tick into the volume ledger (Model B):
					// usage accrues only while the tunnel runs, from
					// these same real cumulative counters.
					usage.noteSample(subCache.get(), down, up)
					lastDown, lastUp, lastAt = down, up, now
				}
			}
		}
	}()
}

func (p *statsPoller) stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	close(p.stopCh)
	p.current = speedSample{}
	p.mu.Unlock()
}

// queryXrayStats runs `xray.exe api statsquery` and returns the cumulative
// proxy outbound uplink/downlink byte totals.
func queryXrayStats() (down, up int64, ok bool) {
	dir, err := exeDir()
	if err != nil {
		return 0, 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "xray.exe"),
		"api", "statsquery",
		"--server=127.0.0.1:10810",
		"--pattern", "outbound>>>proxy>>>traffic")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, false
	}
	var res struct {
		Stat []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return 0, 0, false
	}
	found := false
	for _, s := range res.Stat {
		v, err := strconv.ParseInt(strings.TrimSpace(s.Value), 10, 64)
		if err != nil {
			continue
		}
		switch s.Name {
		case "outbound>>>proxy>>>traffic>>>downlink":
			down = v
			found = true
		case "outbound>>>proxy>>>traffic>>>uplink":
			up = v
			found = true
		}
	}
	return down, up, found
}
