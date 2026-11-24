package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// nmhost tests: framing, validation, and the app-forwarding contract
// against a fake desktop-app HTTP server. No real app is needed.

func TestFramingRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"type":"ping"}`)
	if err := writeMessage(&buf, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readMessage(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

func TestFramingRejectsOversize(t *testing.T) {
	huge := bytes.Repeat([]byte("x"), maxMessageBytes+1)
	if err := writeMessage(&bytes.Buffer{}, huge); err == nil {
		t.Fatalf("oversize write must fail")
	}
}

func fakeApp(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connected": true, "server_id": "s1", "version": "v2.1.0",
		})
	})
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connected": true, "down_bps": 1234, "up_bps": 56,
			"down_total": 1000, "up_total": 200, "available": true,
		})
	})
	mux.HandleFunc("/api/subscription", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan": "gb_20", "subscriber": true, "gift": false,
			"remaining_days": 25, "volume_limited": true, "volume_gb": 20.0,
			"used_bytes": 1000, "used_gb": 0.0, "remaining_gb": 19.9,
			"usage_percent": 1.0, "volume_exceeded": false,
		})
	})
	mux.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URI string `json:"uri"`
			Via string `json:"via"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Via != "extension" {
			t.Errorf("connect must carry via=extension, got %q", req.Via)
		}
		if req.URI == "bad://link" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid server link"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": map[string]any{"connected": true}})
	})
	mux.HandleFunc("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	return httptest.NewServer(mux)
}

func TestHandlePing(t *testing.T) {
	srv := fakeApp(t)
	defer srv.Close()
	h := newHost(srv.URL)
	reply := h.handle(json.RawMessage(`{"type":"ping"}`))
	if reply["ok"] != true || reply["app_running"] != true {
		t.Fatalf("ping: %+v", reply)
	}
}

func TestHandleStatusMergesAppData(t *testing.T) {
	srv := fakeApp(t)
	defer srv.Close()
	h := newHost(srv.URL)
	reply := h.handle(json.RawMessage(`{"type":"status"}`))
	if reply["ok"] != true || reply["connected"] != true {
		t.Fatalf("status: %+v", reply)
	}
	if reply["down_bps"] != float64(1234) || reply["volume_gb"] != float64(20) {
		t.Fatalf("status must forward speeds + volume: %+v", reply)
	}
	if reply["volume_exceeded"] != false {
		t.Fatalf("status volume_exceeded: %+v", reply)
	}
}

func TestHandleConnectForwards(t *testing.T) {
	srv := fakeApp(t)
	defer srv.Close()
	h := newHost(srv.URL)
	ok := h.handle(json.RawMessage(`{"type":"connect","uri":"vless://x"}`))
	if ok["ok"] != true {
		t.Fatalf("connect: %+v", ok)
	}
	bad := h.handle(json.RawMessage(`{"type":"connect","uri":"bad://link"}`))
	if bad["ok"] != false || bad["error"] != "invalid server link" {
		t.Fatalf("connect error must pass through: %+v", bad)
	}
}

func TestHandleDisconnect(t *testing.T) {
	srv := fakeApp(t)
	defer srv.Close()
	h := newHost(srv.URL)
	if reply := h.handle(json.RawMessage(`{"type":"disconnect"}`)); reply["ok"] != true {
		t.Fatalf("disconnect: %+v", reply)
	}
}

func TestHandleValidation(t *testing.T) {
	srv := fakeApp(t)
	defer srv.Close()
	h := newHost(srv.URL)
	cases := []struct {
		raw  string
		want string
	}{
		{`not json`, "bad_message"},
		{`{"type":"explode"}`, "unknown_type"},
		{`{"type":"connect"}`, "bad_uri"},
		{`{"type":"connect","uri":""}`, "bad_uri"},
		{`{"type":"connect","uri":"` + strings.Repeat("x", maxURILength+1) + `"}`, "bad_uri"},
		{`[]`, "bad_message"},
	}
	for _, c := range cases {
		if got := h.handle(json.RawMessage(c.raw)); got["error"] != c.want {
			t.Errorf("handle(%q) error = %v, want %s", c.raw[:min(40, len(c.raw))], got["error"], c.want)
		}
	}
}

func TestHandleAppNotRunning(t *testing.T) {
	// Point at a dead port: every type except ping's app_running flag
	// must answer app_not_running, never a fake success.
	h := newHost("http://127.0.0.1:1")
	ping := h.handle(json.RawMessage(`{"type":"ping"}`))
	if ping["ok"] != true || ping["app_running"] != false {
		t.Fatalf("ping with dead app: %+v", ping)
	}
	for _, raw := range []string{
		`{"type":"status"}`,
		`{"type":"connect","uri":"vless://x"}`,
		`{"type":"disconnect"}`,
	} {
		if got := h.handle(json.RawMessage(raw)); got["error"] != "app_not_running" {
			t.Errorf("handle(%s) = %+v, want app_not_running", raw, got)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
