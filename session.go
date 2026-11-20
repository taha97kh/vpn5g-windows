package main

import (
	"errors"
	"log"
	"sync"
	"time"
)

// connection owns the VPN session: xray child process + system proxy +
// 30-minute timer with auto-disconnect.
type connection struct {
	mu        sync.Mutex
	store     *serverStore
	xray      *xrayRunner
	active    bool
	server    *ServerEntry
	startedAt time.Time
	deadline  time.Time
	stopTimer chan struct{}
}

func newConnection(s *serverStore) *connection {
	return &connection{store: s, xray: &xrayRunner{}}
}

func (c *connection) connect(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		c.disconnectLocked()
	}
	e := c.store.getByID(id)
	if e == nil {
		return errors.New("server not found")
	}
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
	c.deadline = c.startedAt.Add(sessionMinutes * time.Minute)
	c.stopTimer = make(chan struct{})
	go c.watchdog(c.stopTimer, c.deadline)
	log.Printf("connected to %s (%s, %dms)", e.Name, e.Country, e.PingMs)
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
	c.xray.stop()
	_ = setSystemProxy(false)
	c.active = false
	c.server = nil
}

// status returns the JSON-friendly connection state.
func (c *connection) status() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := map[string]any{
		"connected":       c.active,
		"session_minutes": sessionMinutes,
	}
	if c.active {
		remaining := int(time.Until(c.deadline).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		st["server_id"] = c.server.ID
		st["server_name"] = c.server.Name
		st["country"] = c.server.Country
		st["protocol"] = c.server.Protocol
		st["ping_ms"] = c.server.PingMs
		st["remaining_seconds"] = remaining
		st["started_at"] = c.startedAt.Format(time.RFC3339)
	}
	return st
}
