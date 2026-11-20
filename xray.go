package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// xrayRunner manages the xray.exe child process running next to vpn5g.exe.
type xrayRunner struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

func exeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func (r *xrayRunner) start(outbound map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return errors.New("xray already running")
	}
	dir, err := exeDir()
	if err != nil {
		return err
	}
	xrayPath := filepath.Join(dir, "xray.exe")
	if _, err := os.Stat(xrayPath); err != nil {
		return fmt.Errorf("xray.exe not found next to vpn5g.exe (%s): %w", xrayPath, err)
	}
	cfgPath := filepath.Join(dir, "xray-config.json")
	if err := writeXrayConfig(cfgPath, outbound); err != nil {
		return fmt.Errorf("write xray config: %w", err)
	}
	cmd := exec.Command(xrayPath, "-config", cfgPath)
	cmd.Dir = dir
	logPath := filepath.Join(os.TempDir(), "vpn5g-xray.log")
	var logFile *os.File
	if f, ferr := os.Create(logPath); ferr == nil {
		cmd.Stdout = f
		cmd.Stderr = f
		logFile = f
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return fmt.Errorf("start xray: %w", err)
	}
	r.cmd = cmd
	r.done = make(chan struct{})
	done := r.done
	go func() {
		_ = cmd.Wait()
		close(done)
		if logFile != nil {
			_ = logFile.Close()
		}
	}()
	// Confirm xray stays up briefly (e.g. catches a bad config).
	select {
	case <-done:
		return errors.New("xray exited immediately; check %TEMP%\\vpn5g-xray.log")
	case <-time.After(600 * time.Millisecond):
		return nil
	}
}

func (r *xrayRunner) stop() {
	r.mu.Lock()
	cmd := r.cmd
	done := r.done
	r.cmd = nil
	r.done = nil
	r.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
}

func (r *xrayRunner) running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd != nil
}
