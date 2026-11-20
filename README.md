# VPN 5G for Windows — v1.0.0

Native Windows VPN app (no admin rights, no TUN driver). It fetches live
VLESS / VMess / Trojan / Shadowsocks configs, ping-tests them, shows the 20
fastest for free (the rest are VIP), and routes traffic through the official
Xray-core engine via the Windows system proxy.

- **UI:** local web UI at http://127.0.0.1:17890, opened in the default
  browser on launch. Dark X-VPN style, Persian.
- **Engine:** Xray-core v26.11.9 (`xray.exe` next to `vpn5g.exe`, downloaded
  by CI — never committed to this repo).
- **Connection:** Windows system proxy
  (`HKCU\...\Internet Settings`, ProxyEnable + ProxyServer
  `127.0.0.1:10809`), restored on disconnect and on any exit.
- **Session:** 30 minutes, auto-disconnect, live countdown in the UI.
- **Auth:** email OTP register/login via Supabase (same project as the
  Android app). Session stored locally under `%APPDATA%\VPN5G\`.
- **Updates:** checks GitHub releases once a day; a banner offers a
  one-click download + self-restarting update.

## Use on Windows

1. Download `VPN5G-Windows.zip` from the
   [Releases](https://github.com/nefrin2love/vpn5g-windows/releases) page
   (or the Actions artifact) and extract it anywhere.
2. Run `vpn5g.exe`. The UI opens in your browser automatically.
3. Pick a server in the **سرورها** tab and tap it (or tap the power circle
   to connect to the fastest free server). Tap again to disconnect.
4. Sessions last 30 minutes; reconnect afterwards.
5. VIP servers are locked in v1 — get VIP in the Telegram bot:
   https://t.me/VPN5G_IRBot

No installation, no admin rights. To stop completely, close the app (or
disconnect) — the system proxy is restored automatically.

## Config sources (refreshed every 5 minutes)

- https://taha97kh-vpn5g-app.static.hf.space/live_configs.json
  (primary — every entry is fully ping-tested)
- https://taha97kh-vpn5g-app.static.hf.space/sub.txt
- https://raw.githubusercontent.com/0xRadikal/Free-v2ray-Configs/main/verified/configs.txt
  (max 300 sampled lines per refresh)
- https://raw.githubusercontent.com/barry-far/V2ray-Config/main/All_Configs_Sub.txt
  (max 300 sampled lines per refresh)
- plus `config_sources` from `app_config.json` on the same host, if present

Hard rule: **configs with no ping are never shown.** Ping is a TCP connect
to host:port (~3s timeout, concurrent worker pool). Free = the 20 fastest
pinging configs; the rest show as VIP.

## Build on Linux (cross-compile for Windows)

```sh
export PATH=$HOME/go-install/go/bin:$PATH   # or wherever Go 1.25 lives
go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o vpn5g.exe .
```

Then place `xray.exe` (Xray-core v26.11.9 windows-64) next to `vpn5g.exe`:

```sh
curl -sSL -o Xray-windows-64.zip https://github.com/XTLS/Xray-core/releases/download/v26.11.9/Xray-windows-64.zip
unzip -o Xray-windows-64.zip xray.exe
zip -j VPN5G-Windows.zip vpn5g.exe xray.exe README.md
```

## Build on Windows

```powershell
go vet ./...
go build -o vpn5g.exe .
```

## In-app update flow

On launch (and at most once a day while running) the app checks
`https://api.github.com/repos/nefrin2love/vpn5g-windows/releases/latest`
with no auth. If a newer semver tag exists and the release has an asset
named `VPN5G-Windows.zip`, a banner appears: «نسخه جدید اومد» + the new
version + a **بروزرسانی** button.

1. **Download** — the zip streams to `%TEMP%\vpn5g-update\` with live
   progress in the UI. A 404 / no releases / no network means "no update",
   silently — no banner, no error, no retry loop.
2. **Verify** — the zip must open and contain `vpn5g.exe` before anything
   is touched.
3. **Apply** — the app writes `updater.bat` to the temp dir, launches it
   detached, and exits. The updater waits for `vpn5g.exe` (and `xray.exe`)
   to exit, backs up the current `vpn5g.exe`, extracts the zip over the app
   folder, restores the backup on failure, restarts the app, and deletes
   itself. A failed download/apply leaves the button in place with the
   error shown, so you can retry.

API: `GET /api/update/check` (force with `?force=1`),
`POST /api/update/download`, `GET /api/update/status`,
`POST /api/update/apply`.

## Local JSON API (same server, 127.0.0.1:17890)

| Endpoint | Method | Purpose |
|---|---|---|
| `/api/status` | GET | connected? current server, remaining seconds, server count, update info |
| `/api/servers` | GET | ping-tested servers (free flag, country, ping) |
| `/api/connect` | POST | `{"server_id": "..."}` |
| `/api/disconnect` | POST | stop xray, restore proxy |
| `/api/config` | GET | version, ports, session minutes, free limit |
| `/api/auth/send-code` | POST | `{"email": "..."}` — send 6-digit code |
| `/api/auth/verify` | POST | `{"email": "...", "code": "123456"}` |
| `/api/auth/status` | GET | signed-in email or not |
| `/api/auth/logout` | POST | clear local session |
| `/api/update/*` | — | see "In-app update flow" above |

## Layout

```
main.go            entry: HTTP server, browser launch, shutdown handling
store.go           config fetching, sampling, ping-testing, 5-min refresh
parse.go           VLESS/VMess/Trojan/SS parsing, country derivation,
                   Xray outbound builders, Xray config writer
xray.go            xray.exe child-process lifecycle
session.go         connection state + 30-minute auto-disconnect timer
proxy_*.go         Windows system proxy (registry) / non-Windows stub
browser_*.go       open UI in default browser / stub
auth.go            Supabase email OTP, local session file
update.go          GitHub release check, download with progress,
                   zip verification, updater.bat generation
update_*.go        detached updater launch (Windows) / stub
api.go             JSON API handlers
web/               embedded UI (index.html, style.css, app.js)
.github/workflows/build-windows.yml   CI: build + package + smoke test
```

## Notes

- The app starts and serves the UI even when all config sources are
  unreachable (empty server list, no crash).
- The Supabase anon key in `auth.go` is a public client key by design
  (same key the Android app ships). No service_role key or DB password
  is in this repo — never add one.
- `xray.exe` and the built `vpn5g.exe` are never committed (see
  `.gitignore`); CI downloads Xray at build time.

Made by AREF KH — 2026 - 1405
