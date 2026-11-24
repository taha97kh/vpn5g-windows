# VPN 5G for Windows — v2.0.0

Native Windows VPN app (no admin rights, no TUN driver, no console
window). It fetches live VLESS / VMess / Trojan / Shadowsocks configs,
ping-tests them, shows the 20 fastest for free (the rest are VIP), and
routes traffic through the official Xray-core engine via the Windows
system proxy. Exactly like the Android app: same servers, same
subscription, same codes, same ads.

- **UI:** local web UI at http://127.0.0.1:17890, opened in the default
  browser on launch. Dark X-VPN style, Persian. A system-tray icon
  (tray) offers connect / disconnect / open / quit; the exe shows no
  console window (`-H=windowsgui`).
- **Engine:** Xray-core v26.11.9 (`xray.exe` next to `vpn5g.exe`, downloaded
  by CI — never committed to this repo).
- **Connection:** Windows system proxy
  (`HKCU\...\Internet Settings`, ProxyEnable + ProxyServer
  `127.0.0.1:10809`), restored on disconnect and on any exit. The tunnel
  is VERIFIED after connect (exit IP must change); on failure the app
  fails over to the next server automatically (max 3 attempts).
- **Session:** 30 minutes for free users, auto-disconnect, live countdown
  in the UI. Subscribers (and gift tier while it lasts) have NO time
  limit and the full server pool (gift: 50 fastest).
- **Speeds:** live download/upload from Xray's stats API (127.0.0.1:10810);
  unavailable speeds show "—", never a fake zero.
- **Auth:** email + password register/login via Supabase (new project,
  same as Android 1.1.9+). One account per IP + email; the IP guard
  applies only to signup. Session stored locally under `%APPDATA%\VPN5G\`.
  Password recovery is the LINK flow (the link completes in the browser).
- **Subscription & codes:** plans + prices from the server config
  (admin-editable in the bot); subscription codes in the Premium tab,
  gift codes in the Servers tab. Codes are validated server-side first
  (`validate_and_redeem_code`), with the published config list as the
  fallback.
- **Ads:** the 1.1.15 contract — connect #1 has no forced ad; from
  connect #2 a full-screen interstitial (1–20 s, admin-set) shows before
  the VPN starts, and the VPN starts only after the full view. The tray
  never bypasses the ad. User-submitted ads follow their schedule and
  caps; views/clicks are recorded server-side (best effort).
- **Updates:** checks GitHub releases once a day; a banner offers a
  one-click download + self-restarting update.
- **Chrome extension helper:** the extension uses `POST /api/connect`
  (with a chosen server URI) and `GET /api/stats` on 127.0.0.1:17890;
  VLESS/VMess/Trojan/Shadowsocks tunneling only works while this app
  runs — the browser alone cannot run a proxy engine.

## Use on Windows

1. Download `VPN5G-Windows.zip` from the
   [Releases](https://github.com/taha97kh/vpn5g-windows/releases) page
   (or the Actions artifact) and extract it anywhere.
2. Run `vpn5g.exe`. The UI opens in your browser automatically; the app
   also sits in the system tray (no console window).
3. Pick a server in the **سرورها** tab and tap it (or tap the power circle
   to connect to the fastest allowed server). Tap again to disconnect.
4. Free sessions last 30 minutes; reconnect afterwards. Subscribers stay
   connected with no time limit.
5. VIP servers are locked — get VIP in the Telegram bot:
   https://t.me/VPN5G_IRBot

No installation, no admin rights. To stop completely, quit from the tray
(or disconnect) — the system proxy is restored automatically.

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
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H=windowsgui" -o vpn5g.exe .
```

Then place `xray.exe` (Xray-core v26.11.9 windows-64) next to `vpn5g.exe`:

```sh
curl -sSL -o Xray-windows-64.zip https://github.com/XTLS/Xray-core/releases/download/v26.11.9/Xray-windows-64.zip
unzip -o Xray-windows-64.zip xray.exe
zip -j VPN5G-Windows.zip vpn5g.exe xray.exe README.md README-FA.md
```

## Build on Windows

```powershell
go vet ./...
go build -o vpn5g.exe .
```

## In-app update flow

On launch (and at most once a day while running) the app checks
`https://api.github.com/repos/taha97kh/vpn5g-windows/releases/latest`
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
| `/api/status` | GET | connected? current server, remaining seconds, subscription, speeds, update info |
| `/api/servers` | GET | ping-tested servers (free/allowed flags, country, ping) |
| `/api/connect` | POST | `{"server_id": "..."}` or `{"uri": "..."}` (extension); `{"via":"extension"}` skips the helper's own ad gate |
| `/api/disconnect` | POST | stop xray, restore proxy |
| `/api/stats` | GET | live speeds (down/up bps + totals + available) — the Chrome extension helper endpoint |
| `/api/config` | GET | version, ports, session minutes, free limit |
| `/api/subscription` | GET | effective subscription (status, plan, remaining days) |
| `/api/plans` | GET | plans + prices from the server config |
| `/api/redeem` | POST | `{"code": "..."}` — server-first code validation |
| `/api/announcement` | GET | live admin announcement (or null) |
| `/api/ads/viewed` | POST | `{"ad_id": "..."}` — interstitial countdown completed |
| `/api/ads/click` | POST | `{"ad_id": "..."}` — ad tap (never ends the countdown) |
| `/api/auth/signup` | POST | `{"email": "...", "password": "..."}` |
| `/api/auth/login` | POST | `{"email": "...", "password": "..."}` |
| `/api/auth/recover` | POST | `{"email": "..."}` — send the password-reset link |
| `/api/auth/status` | GET | signed-in email or not |
| `/api/auth/logout` | POST | clear local session |
| `/api/update/*` | — | see "In-app update flow" above |

## Layout

```
main.go            entry: HTTP server, browser launch, tray, shutdown handling
store.go           config fetching, sampling, ping-testing, 5-min refresh
parse.go           VLESS/VMess/Trojan/SS parsing, country derivation,
                   Xray outbound builders, Xray config writer (incl. stats API)
xray.go            xray.exe child-process lifecycle
session.go         connection state + tunnel verification + failover +
                   30-minute timer (subscribers unlimited)
stats.go           live speeds from Xray's stats API (xray api statsquery)
proxy_*.go         Windows system proxy (registry) / non-Windows stub
browser_*.go       open UI in default browser / stub
tray.go            Windows system tray (connect/disconnect/open/quit)
tray_other.go      tray stub off Windows
auth.go            Supabase password auth (new project), IP-guarded signup,
                   subscription (server truth + local cache), code RPCs
remoteconfig.go    app_config.json mirror (plans/codes/announcement/ads)
ads.go             1.1.15 ad contract (interstitial, user ads, caps, ledger)
update.go          GitHub release check, download with progress,
                   zip verification, updater.bat generation
update_*.go        detached updater launch (Windows) / stub
api.go             JSON API handlers
web/               embedded UI (index.html, style.css, app.js)
icon.ico           tray icon (transparent 512 shield artwork)
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
