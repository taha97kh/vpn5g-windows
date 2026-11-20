package main

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// writeZip creates a zip with the given entry names (empty contents).
func writeZip(path string, names []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	defer w.Close()
	for _, n := range names {
		fw, err := w.Create(n)
		if err != nil {
			return err
		}
		if _, err := fw.Write([]byte("x")); err != nil {
			return err
		}
	}
	return nil
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.1", "v1.0.0", 1},
		{"v1.0.0", "v1.0.0", 0},
		{"v2.0.0", "v1.9.9", 1},
		{"1.0.0", "v1.0.0", 0},
		{"v1.10.0", "v1.9.0", 1},
		{"v1.0.0-beta", "v1.0.0", 0},
		{"v0.9.9", "v1.0.0", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseLinkVLESS(t *testing.T) {
	raw := "vless://505997ba-cbdb-466e-a3ad-69c6a4553b38@151.101.25.242:80?path=/x&security=none&encryption=none&host=example.com&type=ws#Test"
	e, err := parseLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.Protocol != "vless" || e.Host != "151.101.25.242" || e.Port != 80 {
		t.Errorf("unexpected entry: %+v", e)
	}
	out, err := buildOutbound(e)
	if err != nil {
		t.Fatal(err)
	}
	if out["protocol"] != "vless" {
		t.Errorf("outbound protocol = %v", out["protocol"])
	}
}

func TestParseLinkTrojan(t *testing.T) {
	raw := "trojan://pass123@1.2.3.4:443?sni=example.com#TrojanTest"
	e, err := parseLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.Protocol != "trojan" || e.Port != 443 {
		t.Errorf("unexpected entry: %+v", e)
	}
	if _, err := buildOutbound(e); err != nil {
		t.Fatal(err)
	}
}

func TestParseLinkSS(t *testing.T) {
	// ss://base64(aes-256-gcm:secret)@5.6.7.8:8388#SSTest
	raw := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:secret")) + "@5.6.7.8:8388#SSTest"
	e, err := parseLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.Protocol != "ss" || e.Host != "5.6.7.8" || e.Port != 8388 {
		t.Errorf("unexpected entry: %+v", e)
	}
	out, err := buildOutbound(e)
	if err != nil {
		t.Fatal(err)
	}
	if out["protocol"] != "shadowsocks" {
		t.Errorf("outbound protocol = %v", out["protocol"])
	}
}

func TestParseLinkVMess(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"v": "2", "ps": "VMTest", "add": "9.9.9.9", "port": "443",
		"id": "3786e67a-41eb-4b92-b31e-417e89493b93", "aid": "0",
		"net": "ws", "type": "none", "host": "example.com",
		"path": "/ws", "tls": "tls", "sni": "example.com",
	})
	raw := "vmess://" + base64.StdEncoding.EncodeToString(payload)
	e, err := parseLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.Protocol != "vmess" || e.Host != "9.9.9.9" || e.Port != 443 {
		t.Errorf("unexpected entry: %+v", e)
	}
	if _, err := buildOutbound(e); err != nil {
		t.Fatal(err)
	}
}

func TestCountryFromRemark(t *testing.T) {
	cases := []struct{ remark, want string }{
		{"🇩🇪 Berlin", "Germany"},
		{"🇺🇸 USA", "USA"},
		{"🇫🇷 فرانسه", "France"},
		{"Germany - fast", "Germany"},
		{"Telegram @VPNcrackNET", "Other"},
		{"", "Other"},
	}
	for _, c := range cases {
		if got := countryFromRemark(c.remark); got != c.want {
			t.Errorf("countryFromRemark(%q) = %q, want %q", c.remark, got, c.want)
		}
	}
}

func TestVerifyUpdateZip(t *testing.T) {
	dir := t.TempDir()
	good := dir + "/good.zip"
	if err := writeZip(good, []string{"vpn5g.exe", "xray.exe", "README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := verifyUpdateZip(good); err != nil {
		t.Errorf("good zip rejected: %v", err)
	}
	bad := dir + "/bad.zip"
	if err := writeZip(bad, []string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := verifyUpdateZip(bad); err == nil {
		t.Errorf("zip without vpn5g.exe was accepted")
	}
	if err := verifyUpdateZip(dir + "/missing.zip"); err == nil {
		t.Errorf("missing zip was accepted")
	}
}
