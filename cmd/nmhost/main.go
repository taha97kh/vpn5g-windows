// Command nmhost is the TAHA VPN 5G Native Messaging host: the bridge
// between the Chrome extension and the Windows desktop app.
//
// Chrome starts one nmhost process per message (sendNativeMessage), speaks
// to it over stdin/stdout with Chrome's framing — a 4-byte little-endian
// length followed by one JSON message, max 1 MB — and closes stdin when
// done; the host then exits. The host itself holds NO state, NO account,
// and NO secrets: every request is forwarded to the desktop app's local
// HTTP API on 127.0.0.1:17890, which is the single source of truth for
// connection state, subscription, and volume.
//
// Authorization: Chrome enforces the host manifest's allowed_origins —
// only the TAHA VPN 5G extension (fixed id, see the manifest the
// installer writes) may talk to this host. The host additionally accepts
// only the four message types below and validates every field; anything
// else gets {"ok":false,...} and no action is taken. All logging goes to
// stderr — stdout is the protocol and must stay clean.
//
// Requests (extension -> host):
//
//	{"type":"ping"}                       -> liveness + app presence
//	{"type":"status"}                     -> connection + speeds + subscription + volume
//	{"type":"connect","uri":"<link>"}     -> connect the desktop app to this server
//	{"type":"disconnect"}                 -> disconnect the desktop app
//
// Responses are always JSON objects with a boolean "ok". When the desktop
// app is not running, every type except ping answers
// {"ok":false,"error":"app_not_running"} so the extension can show the
// install/run guide instead of a fake state.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// appBaseURL is the desktop app's local API. Loopback only — the app
// itself only listens on 127.0.0.1.
const appBaseURL = "http://127.0.0.1:17890"

// maxMessageBytes is Chrome's native-messaging limit (1 MB).
const maxMessageBytes = 1024 * 1024

// maxURILength bounds a server link; real links are far shorter.
const maxURILength = 8192

const hostVersion = "1"

type host struct {
	appBase string
	client  *http.Client
}

func newHost(appBase string) *host {
	return &host{
		appBase: strings.TrimRight(appBase, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// readMessage reads one length-prefixed JSON message.
func readMessage(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(lenBuf[:])
	if n == 0 || n > maxMessageBytes {
		return nil, fmt.Errorf("invalid message length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// writeMessage writes one length-prefixed JSON message.
func writeMessage(w io.Writer, payload []byte) error {
	if len(payload) > maxMessageBytes {
		return fmt.Errorf("message too large: %d bytes", len(payload))
	}
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func errReply(code string) map[string]any {
	return map[string]any{"ok": false, "error": code}
}

func (h *host) appGet(path string) (map[string]any, error) {
	resp, err := h.client.Get(h.appBase + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("app returned %s", resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *host) appPost(path string, body any) (map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(data)
	}
	resp, err := h.client.Post(h.appBase+path, "application/json", rdr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// appRunning reports whether the desktop app answers on loopback.
func (h *host) appRunning() bool {
	_, err := h.appGet("/api/status")
	return err == nil
}

// handle validates one decoded request and returns the reply. It never
// panics on bad input and never performs an action for an unknown type.
func (h *host) handle(raw json.RawMessage) map[string]any {
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		return errReply("bad_message")
	}
	typ, _ := req["type"].(string)
	switch typ {
	case "ping":
		return map[string]any{
			"ok": true, "host": "taha-vpn5g-nmhost", "version": hostVersion,
			"app_running": h.appRunning(),
		}
	case "status":
		return h.handleStatus()
	case "connect":
		uri, _ := req["uri"].(string)
		uri = strings.TrimSpace(uri)
		if uri == "" || len(uri) > maxURILength {
			return errReply("bad_uri")
		}
		return h.handleConnect(uri)
	case "disconnect":
		return h.handleDisconnect()
	default:
		return errReply("unknown_type")
	}
}

func (h *host) handleStatus() map[string]any {
	status, err := h.appGet("/api/status")
	if err != nil {
		return errReply("app_not_running")
	}
	stats, _ := h.appGet("/api/stats")
	sub, _ := h.appGet("/api/subscription")

	out := map[string]any{
		"ok": true, "app_running": true,
		"connected": status["connected"],
		"server_id": status["server_id"],
		"version":   status["version"],
	}
	// Speeds + totals (absent when the tunnel is down — never faked).
	if stats != nil {
		out["down_bps"] = stats["down_bps"]
		out["up_bps"] = stats["up_bps"]
		out["down_total"] = stats["down_total"]
		out["up_total"] = stats["up_total"]
		out["stats_available"] = stats["available"]
	}
	// Subscription + volume (Model B), straight from the app.
	if sub != nil {
		out["plan"] = sub["plan"]
		out["subscriber"] = sub["subscriber"]
		out["gift"] = sub["gift"]
		out["remaining_days"] = sub["remaining_days"]
		out["volume_limited"] = sub["volume_limited"]
		out["volume_gb"] = sub["volume_gb"]
		out["used_bytes"] = sub["used_bytes"]
		out["used_gb"] = sub["used_gb"]
		out["remaining_gb"] = sub["remaining_gb"]
		out["usage_percent"] = sub["usage_percent"]
		out["volume_exceeded"] = sub["volume_exceeded"]
	}
	return out
}

func (h *host) handleConnect(uri string) map[string]any {
	res, err := h.appPost("/api/connect",
		map[string]any{"uri": uri, "via": "extension"})
	if err != nil {
		return errReply("app_not_running")
	}
	if ok, _ := res["ok"].(bool); ok {
		return map[string]any{"ok": true, "status": res["status"]}
	}
	// Pass the app's own error through (already user-safe Persian or a
	// stable code like "vip" / "ad_required"); never invent one.
	if e, _ := res["error"].(string); e != "" {
		out := map[string]any{"ok": false, "error": e}
		if res["message"] != nil {
			out["message"] = res["message"]
		}
		return out
	}
	return errReply("connect_failed")
}

func (h *host) handleDisconnect() map[string]any {
	if _, err := h.appPost("/api/disconnect", nil); err != nil {
		return errReply("app_not_running")
	}
	return map[string]any{"ok": true}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	h := newHost(appBaseURL)
	for {
		raw, err := readMessage(os.Stdin)
		if err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				log.Printf("nmhost read: %v", err)
			}
			return // Chrome closed stdin: done.
		}
		reply := h.handle(raw)
		data, err := json.Marshal(reply)
		if err != nil {
			log.Printf("nmhost marshal: %v", err)
			return
		}
		if err := writeMessage(os.Stdout, data); err != nil {
			log.Printf("nmhost write: %v", err)
			return
		}
	}
}
