package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// In-app update: check GitHub releases, download VPN5G-Windows.zip, and
// apply it via a detached updater script that replaces the app files and
// restarts the app. The repo does not exist yet — a 404 / no releases is
// treated as "no update", silently. No network is also silent: no banner,
// no error, no retry loop. The update path never bricks the install: the
// zip is downloaded to a temp dir and verified (it must extract and contain
// vpn5g.exe) before anything is replaced.

const (
	githubLatestReleaseURL = "https://api.github.com/repos/nefrin2love/vpn5g-windows/releases/latest"
	updateAssetName        = "VPN5G-Windows.zip"
	updateCheckInterval    = 24 * time.Hour
)

const (
	updateIdle        = "idle"
	updateDownloading = "downloading"
	updateDownloaded  = "downloaded"
	updateFailed      = "failed"
	updateApplying    = "applying"
)

type updateState struct {
	mu            sync.Mutex
	available     bool
	latestVersion string
	downloadURL   string
	checkedAt     time.Time
	lastAttempt   time.Time
	state         string
	downloaded    int64
	total         int64
	errMsg        string
}

var upd = &updateState{state: updateIdle}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// updateCheckLoop checks once at launch, then at most once a day while
// running (checkForUpdate enforces the 24h minimum interval).
func updateCheckLoop() {
	checkForUpdate(false)
	for range time.Tick(time.Hour) {
		checkForUpdate(false)
	}
}

// checkForUpdate queries the latest GitHub release with NO auth. A 404, no
// releases, no matching asset, or no network all mean "no update" —
// silently. force bypasses the once-a-day cache.
func checkForUpdate(force bool) {
	upd.mu.Lock()
	if !force && !upd.lastAttempt.IsZero() && time.Since(upd.lastAttempt) < updateCheckInterval {
		upd.mu.Unlock()
		return
	}
	upd.lastAttempt = time.Now()
	upd.mu.Unlock()

	req, err := http.NewRequest(http.MethodGet, githubLatestReleaseURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "VPN5G-Windows/"+appVersion)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("update check: %v (silent, no banner)", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 404 = repo/releases don't exist yet. Silent: no update.
		return
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return
	}
	downloadURL := ""
	for _, a := range rel.Assets {
		if a.Name == updateAssetName {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	upd.mu.Lock()
	defer upd.mu.Unlock()
	upd.checkedAt = time.Now()
	upd.latestVersion = rel.TagName
	if downloadURL != "" && rel.TagName != "" && compareVersions(rel.TagName, appVersion) > 0 {
		upd.available = true
		upd.downloadURL = downloadURL
		log.Printf("update available: %s -> %s", appVersion, rel.TagName)
	} else {
		upd.available = false
		upd.downloadURL = ""
	}
}

// compareVersions compares semver strings, ignoring a leading "v" and any
// pre-release/build suffix. Returns -1, 0, or 1.
func compareVersions(a, b string) int {
	norm := func(s string) []int {
		s = strings.TrimSpace(strings.TrimPrefix(s, "v"))
		if i := strings.IndexAny(s, "-+"); i >= 0 {
			s = s[:i]
		}
		parts := strings.Split(s, ".")
		out := make([]int, len(parts))
		for i, p := range parts {
			n, _ := strconv.Atoi(strings.TrimSpace(p))
			out[i] = n
		}
		return out
	}
	pa, pb := norm(a), norm(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

func (u *updateState) snapshot() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return map[string]any{
		"current_version":  appVersion,
		"update_available": u.available,
		"latest_version":   u.latestVersion,
		"checked_at":       u.checkedAt.Format(time.RFC3339),
		"state":            u.state,
		"downloaded":       u.downloaded,
		"total":            u.total,
		"error":            u.errMsg,
	}
}

func updateTempDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "vpn5g-update")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func updateZipPath() (string, error) {
	dir, err := updateTempDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, updateAssetName), nil
}

// startDownload begins the release-zip download in the background.
// Progress is visible via /api/update/status. Safe to retry after failure.
func startDownload() error {
	upd.mu.Lock()
	if upd.downloadURL == "" {
		upd.mu.Unlock()
		return fmt.Errorf("no update download available")
	}
	if upd.state == updateDownloading {
		upd.mu.Unlock()
		return fmt.Errorf("download already in progress")
	}
	upd.state = updateDownloading
	upd.downloaded = 0
	upd.total = 0
	upd.errMsg = ""
	url := upd.downloadURL
	upd.mu.Unlock()

	go func() {
		if err := downloadUpdate(url); err != nil {
			upd.mu.Lock()
			upd.state = updateFailed
			upd.errMsg = err.Error()
			upd.mu.Unlock()
			log.Printf("update download failed: %v", err)
		}
	}()
	return nil
}

type countingWriter struct {
	w io.Writer
	n *int64
	m *sync.Mutex
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.m.Lock()
	*c.n += int64(n)
	c.m.Unlock()
	return n, err
}

func downloadUpdate(url string) error {
	zipPath, err := updateZipPath()
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	upd.mu.Lock()
	upd.total = resp.ContentLength
	upd.mu.Unlock()

	tmpPath := zipPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	var downloaded int64
	cw := &countingWriter{w: f, n: &downloaded, m: &upd.mu}
	// Keep the shared counter in sync while copying.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				upd.mu.Lock()
				upd.downloaded = downloaded
				upd.mu.Unlock()
			case <-done:
				return
			}
		}
	}()
	_, copyErr := io.Copy(cw, resp.Body)
	close(done)
	upd.mu.Lock()
	upd.downloaded = downloaded
	upd.mu.Unlock()
	_ = f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return copyErr
	}
	if err := os.Rename(tmpPath, zipPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	// Verify BEFORE reporting success: the zip must extract and contain
	// vpn5g.exe, otherwise the install is never touched.
	if err := verifyUpdateZip(zipPath); err != nil {
		_ = os.Remove(zipPath)
		return err
	}
	upd.mu.Lock()
	upd.state = updateDownloaded
	upd.mu.Unlock()
	log.Printf("update %s downloaded and verified", upd.latestVersion)
	return nil
}

// verifyUpdateZip opens the zip and requires a vpn5g.exe entry.
func verifyUpdateZip(zipPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("invalid update zip: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		if strings.EqualFold(filepath.Base(f.Name), "vpn5g.exe") {
			return nil
		}
	}
	return fmt.Errorf("update zip does not contain vpn5g.exe")
}

// applyUpdate verifies the downloaded zip again, writes the updater script,
// launches it detached, and returns. The caller must exit the app promptly
// afterwards so the updater can replace the files.
func applyUpdate() error {
	upd.mu.Lock()
	if upd.state != updateDownloaded {
		upd.mu.Unlock()
		return fmt.Errorf("no verified update downloaded (state: %s)", upd.state)
	}
	upd.state = updateApplying
	upd.mu.Unlock()

	zipPath, err := updateZipPath()
	if err != nil {
		return failApply(err)
	}
	if err := verifyUpdateZip(zipPath); err != nil {
		return failApply(err)
	}
	dir, err := exeDir()
	if err != nil {
		return failApply(err)
	}
	batPath, err := writeUpdaterScript(dir, zipPath)
	if err != nil {
		return failApply(err)
	}
	if err := launchUpdater(batPath); err != nil {
		return failApply(err)
	}
	log.Printf("updater launched, exiting for update to %s", upd.latestVersion)
	return nil
}

func failApply(err error) error {
	upd.mu.Lock()
	upd.state = updateFailed
	upd.errMsg = err.Error()
	upd.mu.Unlock()
	return err
}

// writeUpdaterScript writes updater.bat next to the temp zip. The script:
//  1. waits until vpn5g.exe has exited (up to ~30s, then force-kills it),
//  2. kills xray.exe if still running,
//  3. backs up the current vpn5g.exe, extracts the zip over the app dir,
//  4. on extract failure restores the backup; on success deletes it,
//  5. restarts vpn5g.exe, cleans up the zip, and deletes itself.
func writeUpdaterScript(appDir, zipPath string) (string, error) {
	dir, err := updateTempDir()
	if err != nil {
		return "", err
	}
	batPath := filepath.Join(dir, "updater.bat")
	script := "@echo off\r\n" +
		"setlocal\r\n" +
		"set \"APP_DIR=" + appDir + "\"\r\n" +
		"set \"ZIP=" + zipPath + "\"\r\n" +
		"set \"BAT=" + batPath + "\"\r\n" +
		"\r\n" +
		"rem Wait for the old app to exit (it exits right after launching us).\r\n" +
		"set /a TRIES=0\r\n" +
		":waitloop\r\n" +
		"tasklist /FI \"IMAGENAME eq vpn5g.exe\" 2>nul | find /I \"vpn5g.exe\" >nul\r\n" +
		"if errorlevel 1 goto gone\r\n" +
		"set /a TRIES+=1\r\n" +
		"if %TRIES% GEQ 30 taskkill /F /IM vpn5g.exe >nul 2>&1\r\n" +
		"if %TRIES% GEQ 32 goto gone\r\n" +
		"timeout /t 1 /nobreak >nul\r\n" +
		"goto waitloop\r\n" +
		":gone\r\n" +
		"taskkill /F /IM xray.exe >nul 2>&1\r\n" +
		"timeout /t 1 /nobreak >nul\r\n" +
		"\r\n" +
		"rem Back up the current exe so a failed extract can be rolled back.\r\n" +
		"if exist \"%APP_DIR%\\vpn5g.exe.bak\" del /F /Q \"%APP_DIR%\\vpn5g.exe.bak\"\r\n" +
		"if exist \"%APP_DIR%\\vpn5g.exe\" ren \"%APP_DIR%\\vpn5g.exe\" \"vpn5g.exe.bak\"\r\n" +
		"powershell -NoProfile -Command \"Expand-Archive -LiteralPath '%ZIP%' -DestinationPath '%APP_DIR%' -Force\"\r\n" +
		"if errorlevel 1 goto rollback\r\n" +
		"if not exist \"%APP_DIR%\\vpn5g.exe\" goto rollback\r\n" +
		"del /F /Q \"%APP_DIR%\\vpn5g.exe.bak\" >nul 2>&1\r\n" +
		"goto restart\r\n" +
		":rollback\r\n" +
		"if exist \"%APP_DIR%\\vpn5g.exe\" del /F /Q \"%APP_DIR%\\vpn5g.exe\" >nul 2>&1\r\n" +
		"if exist \"%APP_DIR%\\vpn5g.exe.bak\" ren \"%APP_DIR%\\vpn5g.exe.bak\" \"vpn5g.exe\"\r\n" +
		":restart\r\n" +
		"start \"\" \"%APP_DIR%\\vpn5g.exe\"\r\n" +
		"del /F /Q \"%ZIP%\" >nul 2>&1\r\n" +
		"del /F /Q \"%ZIP%.tmp\" >nul 2>&1\r\n" +
		"(goto) 2>nul & del \"%BAT%\"\r\n"
	if err := os.WriteFile(batPath, []byte(script), 0o600); err != nil {
		return "", err
	}
	return batPath, nil
}
