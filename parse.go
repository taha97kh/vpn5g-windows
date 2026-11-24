package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// parseLink parses a VLESS / VMess / Trojan / Shadowsocks share link into a
// ServerEntry. Country is derived from the remark's flag emoji or name.
func parseLink(raw string) (*ServerEntry, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	remark, _ := url.PathUnescape(u.Fragment)

	switch strings.ToLower(u.Scheme) {
	case "vless", "trojan":
		host := u.Hostname()
		port, _ := strconv.Atoi(u.Port())
		if host == "" || port == 0 {
			return nil, errors.New("missing host/port")
		}
		return &ServerEntry{
			ID:       entryID(raw),
			Protocol: strings.ToLower(u.Scheme),
			Country:  countryFromRemark(remark),
			Name:     displayName(remark, host),
			URI:      raw,
			Host:     host,
			Port:     port,
		}, nil
	case "vmess":
		vm, err := parseVMess(raw)
		if err != nil {
			return nil, err
		}
		if vm.Add == "" || vm.PortNum() == 0 {
			return nil, errors.New("vmess missing host/port")
		}
		return &ServerEntry{
			ID:       entryID(raw),
			Protocol: "vmess",
			Country:  countryFromRemark(vm.Ps),
			Name:     displayName(vm.Ps, vm.Add),
			URI:      raw,
			Host:     vm.Add,
			Port:     vm.PortNum(),
		}, nil
	case "ss":
		host, port, method, password, err := parseSS(raw)
		if err != nil {
			return nil, err
		}
		_ = method
		_ = password
		return &ServerEntry{
			ID:       entryID(raw),
			Protocol: "ss",
			Country:  countryFromRemark(remark),
			Name:     displayName(remark, host),
			URI:      raw,
			Host:     host,
			Port:     port,
		}, nil
	}
	return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
}

func displayName(remark, host string) string {
	remark = strings.TrimSpace(remark)
	if remark != "" {
		return remark
	}
	return host
}

// vmessJSON is the decoded vmess:// payload. Port/Aid may be string or number.
type vmessJSON struct {
	Add  string          `json:"add"`
	Port json.RawMessage `json:"port"`
	ID   string          `json:"id"`
	Aid  json.RawMessage `json:"aid"`
	Net  string          `json:"net"`
	Type string          `json:"type"`
	Host string          `json:"host"`
	Path string          `json:"path"`
	TLS  string          `json:"tls"`
	SNI  string          `json:"sni"`
	FP   string          `json:"fp"`
	Ps   string          `json:"ps"`
	Alpn string          `json:"alpn"`
}

func (v *vmessJSON) PortNum() int { return rawPort(v.Port) }
func (v *vmessJSON) AidNum() int  { return rawPort(v.Aid) }

func rawPort(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		return n
	}
	return 0
}

func parseVMess(raw string) (*vmessJSON, error) {
	rest := strings.TrimPrefix(raw, "vmess://")
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	decoded, err := decodeB64Flexible(strings.TrimSpace(rest))
	if err != nil {
		return nil, err
	}
	var vm vmessJSON
	if err := json.Unmarshal(decoded, &vm); err != nil {
		return nil, err
	}
	vm.Port = json.RawMessage(strconv.Itoa(vm.PortNum()))
	vm.Aid = json.RawMessage(strconv.Itoa(vm.AidNum()))
	return &vm, nil
}

// parseSS handles ss:// forms:
//
//	ss://base64(method:password)@host:port#remark
//	ss://base64(method:password@host:port)#remark
//	ss://method:password@host:port#remark
func parseSS(raw string) (host string, port int, method, password string, err error) {
	rest := strings.TrimPrefix(raw, "ss://")
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		userinfo := rest[:at]
		hostport := rest[at+1:]
		if decoded, derr := decodeB64Flexible(userinfo); derr == nil && strings.Contains(string(decoded), ":") {
			userinfo = string(decoded)
		}
		if u, uerr := url.PathUnescape(userinfo); uerr == nil {
			userinfo = u
		}
		i := strings.Index(userinfo, ":")
		if i < 0 {
			return "", 0, "", "", errors.New("ss missing method:password")
		}
		method, password = userinfo[:i], userinfo[i+1:]
		host, port, err = splitHostPort(hostport)
		return host, port, method, password, err
	}
	decoded, derr := decodeB64Flexible(rest)
	if derr != nil {
		return "", 0, "", "", derr
	}
	s := string(decoded)
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return "", 0, "", "", errors.New("ss missing @")
	}
	userinfo := s[:at]
	i := strings.Index(userinfo, ":")
	if i < 0 {
		return "", 0, "", "", errors.New("ss missing method:password")
	}
	method, password = userinfo[:i], userinfo[i+1:]
	host, port, err = splitHostPort(s[at+1:])
	return host, port, method, password, err
}

func splitHostPort(hostport string) (string, int, error) {
	hostport = strings.Trim(hostport, "[]")
	// Last colon separates the port (handles IPv6 in brackets too).
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return "", 0, errors.New("missing port")
	}
	port, err := strconv.Atoi(hostport[i+1:])
	if err != nil {
		return "", 0, err
	}
	return strings.Trim(hostport[:i], "[]"), port, nil
}

// ---------------------------------------------------------------------------
// Xray outbound builders
// ---------------------------------------------------------------------------

// buildOutbound converts a ServerEntry's share link into an Xray outbound
// object (as generic JSON maps).
func buildOutbound(e *ServerEntry) (map[string]any, error) {
	u, err := url.Parse(e.URI)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "vless":
		return vlessOutbound(u), nil
	case "trojan":
		return trojanOutbound(u), nil
	case "vmess":
		vm, err := parseVMess(e.URI)
		if err != nil {
			return nil, err
		}
		return vmessOutbound(vm), nil
	case "ss":
		host, port, method, password, err := parseSS(e.URI)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"protocol": "shadowsocks",
			"tag":      "proxy",
			"settings": map[string]any{
				"servers": []any{
					map[string]any{
						"address":  host,
						"port":     port,
						"method":   method,
						"password": password,
						"level":    0,
					},
				},
			},
			"streamSettings": map[string]any{"network": "tcp"},
		}, nil
	}
	return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
}

func vlessOutbound(u *url.URL) map[string]any {
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	q := u.Query()
	user := map[string]any{"id": u.User.Username(), "encryption": "none", "level": 0}
	if flow := q.Get("flow"); flow != "" {
		user["flow"] = flow
	}
	return map[string]any{
		"protocol":       "vless",
		"tag":            "proxy",
		"settings":       map[string]any{"vnext": []any{map[string]any{"address": host, "port": port, "users": []any{user}}}},
		"streamSettings": streamSettings(q, host),
	}
}

func trojanOutbound(u *url.URL) map[string]any {
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	q := u.Query()
	password, _ := u.User.Password()
	if password == "" {
		password = u.User.Username()
	}
	security := firstNonEmpty(q.Get("security"), "tls")
	return map[string]any{
		"protocol": "trojan",
		"tag":      "proxy",
		"settings": map[string]any{"servers": []any{map[string]any{
			"address": host, "port": port, "password": password, "level": 0,
		}}},
		"streamSettings": streamSettings(q, host, security),
	}
}

func vmessOutbound(vm *vmessJSON) map[string]any {
	q := url.Values{}
	q.Set("type", vm.Net)
	q.Set("security", "tls")
	if vm.TLS == "" || vm.TLS == "none" {
		q.Set("security", "none")
	}
	q.Set("host", vm.Host)
	q.Set("path", vm.Path)
	q.Set("sni", firstNonEmpty(vm.SNI, vm.Host))
	q.Set("fp", vm.FP)
	if vm.Alpn != "" {
		q.Set("alpn", vm.Alpn)
	}
	user := map[string]any{"id": vm.ID, "alterId": vm.AidNum(), "security": "auto", "level": 0}
	return map[string]any{
		"protocol":       "vmess",
		"tag":            "proxy",
		"settings":       map[string]any{"vnext": []any{map[string]any{"address": vm.Add, "port": vm.PortNum(), "users": []any{user}}}},
		"streamSettings": streamSettings(q, vm.Add),
	}
}

// streamSettings builds Xray streamSettings from link query params.
// extraSecurity, when given, overrides the "security" query param (Trojan
// defaults to tls).
func streamSettings(q url.Values, host string, extraSecurity ...string) map[string]any {
	network := firstNonEmpty(q.Get("type"), "tcp")
	security := firstNonEmpty(q.Get("security"), "none")
	if len(extraSecurity) > 0 && extraSecurity[0] != "" {
		security = extraSecurity[0]
	}
	s := map[string]any{"network": network, "security": security}
	switch security {
	case "tls":
		tls := map[string]any{
			"serverName":    firstNonEmpty(q.Get("sni"), host),
			"allowInsecure": q.Get("allowInsecure") == "1" || q.Get("insecure") == "1",
		}
		if fp := firstNonEmpty(q.Get("fp"), "chrome"); fp != "" {
			tls["fingerprint"] = fp
		}
		if alpn := q.Get("alpn"); alpn != "" {
			tls["alpn"] = strings.Split(alpn, ",")
		}
		s["tlsSettings"] = tls
	case "reality":
		s["realitySettings"] = map[string]any{
			"serverName":  firstNonEmpty(q.Get("sni"), host),
			"fingerprint": firstNonEmpty(q.Get("fp"), "chrome"),
			"publicKey":   q.Get("pbk"),
			"shortId":     q.Get("sid"),
			"show":        false,
		}
	}
	switch network {
	case "ws":
		s["wsSettings"] = map[string]any{
			"path":    firstNonEmpty(q.Get("path"), "/"),
			"headers": map[string]any{"Host": firstNonEmpty(q.Get("host"), host)},
		}
	case "grpc":
		s["grpcSettings"] = map[string]any{
			"serviceName": firstNonEmpty(q.Get("path"), ""),
			"multiMode":   q.Get("mode") == "multi",
		}
	case "xhttp":
		s["xhttpSettings"] = map[string]any{
			"path": firstNonEmpty(q.Get("path"), "/"),
			"host": firstNonEmpty(q.Get("host"), host),
		}
	case "tcp":
		if q.Get("headerType") == "http" {
			s["tcpSettings"] = map[string]any{"header": map[string]any{
				"type": "http",
				"request": map[string]any{
					"path":    []string{firstNonEmpty(q.Get("path"), "/")},
					"headers": map[string]any{"Host": []string{firstNonEmpty(q.Get("host"), host)}},
				},
			}}
		}
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// writeXrayConfig writes the full Xray config: HTTP inbound on
// 127.0.0.1:10809, the selected server as the proxy outbound, plus direct
// (private IPs) and blocked fallbacks. It also enables outbound traffic
// stats and exposes the StatsService on 127.0.0.1:10810 so the app can
// show live tunnel speeds (see stats.go). The stats API is local-only.
func writeXrayConfig(path string, outbound map[string]any) error {
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"policy": map[string]any{"levels": map[string]any{
			"0": map[string]any{
				"statsOutboundUplink":   true,
				"statsOutboundDownlink": true,
			},
		}},
		"api": map[string]any{
			"tag":      "api",
			"services": []string{"StatsService"},
		},
		"inbounds": []any{
			map[string]any{
				"port":     proxyPort,
				"listen":   "127.0.0.1",
				"protocol": "http",
				"settings": map[string]any{},
				"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls"}},
			},
			map[string]any{
				"port":     xrayStatsPort,
				"listen":   "127.0.0.1",
				"protocol": "dokodemo-door",
				"settings": map[string]any{"address": "127.0.0.1"},
				"tag":      "api",
			},
		},
		"outbounds": []any{
			outbound,
			map[string]any{"protocol": "freedom", "tag": "direct"},
			map[string]any{"protocol": "blackhole", "tag": "blocked"},
		},
		"routing": map[string]any{
			"domainStrategy": "IPIfNonMatch",
			"rules": []any{
				map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
				map[string]any{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "direct"},
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ---------------------------------------------------------------------------
// Country derivation from remark
// ---------------------------------------------------------------------------

// flagToCountry maps ISO codes (from flag emoji regional indicators) to names.
var flagToCountry = map[string]string{
	"US": "USA", "DE": "Germany", "FR": "France", "NL": "Netherlands",
	"GB": "UK", "CA": "Canada", "JP": "Japan", "SG": "Singapore",
	"TR": "Turkey", "IR": "Iran", "AE": "UAE", "FI": "Finland",
	"SE": "Sweden", "CH": "Switzerland", "PL": "Poland", "RU": "Russia",
	"IN": "India", "KR": "South Korea", "AU": "Australia", "BR": "Brazil",
	"IT": "Italy", "ES": "Spain", "AT": "Austria", "NO": "Norway",
	"DK": "Denmark", "LT": "Lithuania", "LV": "Latvia", "EE": "Estonia",
	"RO": "Romania", "BG": "Bulgaria", "HK": "Hong Kong", "TW": "Taiwan",
	"TH": "Thailand", "VN": "Vietnam", "MY": "Malaysia", "ID": "Indonesia",
	"PH": "Philippines", "ZA": "South Africa", "MX": "Mexico", "AR": "Argentina",
	"CL": "Chile", "CO": "Colombia", "PE": "Peru", "UA": "Ukraine",
	"GR": "Greece", "PT": "Portugal", "IE": "Ireland", "CZ": "Czechia",
	"HU": "Hungary", "HR": "Croatia", "RS": "Serbia", "IL": "Israel",
	"SA": "Saudi Arabia", "QA": "Qatar", "KW": "Kuwait", "BH": "Bahrain",
	"OM": "Oman", "JO": "Jordan", "LB": "Lebanon", "EG": "Egypt",
	"NG": "Nigeria", "KE": "Kenya", "MA": "Morocco", "TN": "Tunisia",
	"AL": "Albania", "MK": "Macedonia", "BA": "Bosnia", "GE": "Georgia",
	"AM": "Armenia", "AZ": "Azerbaijan", "KZ": "Kazakhstan", "UZ": "Uzbekistan",
	"MN": "Mongolia", "NP": "Nepal", "LK": "Sri Lanka", "BD": "Bangladesh",
	"PK": "Pakistan", "KH": "Cambodia", "LA": "Laos", "MM": "Myanmar",
	"NZ": "New Zealand", "LU": "Luxembourg", "BE": "Belgium", "IS": "Iceland",
}

var nameToCountry = map[string]string{
	"germany": "Germany", "usa": "USA", "america": "USA", "united states": "USA",
	"netherlands": "Netherlands", "france": "France", "uk": "UK", "england": "UK",
	"britain": "UK", "canada": "Canada", "japan": "Japan", "singapore": "Singapore",
	"turkey": "Turkey", "turkiye": "Turkey", "finland": "Finland", "sweden": "Sweden",
	"switzerland": "Switzerland", "poland": "Poland", "russia": "Russia",
	"india": "India", "korea": "South Korea", "australia": "Australia",
	"brazil": "Brazil", "italy": "Italy", "spain": "Spain", "austria": "Austria",
	"norway": "Norway", "denmark": "Denmark", "lithuania": "Lithuania",
	"latvia": "Latvia", "estonia": "Estonia", "romania": "Romania",
	"bulgaria": "Bulgaria", "hong kong": "Hong Kong", "taiwan": "Taiwan",
	"thailand": "Thailand", "vietnam": "Vietnam", "malaysia": "Malaysia",
	"indonesia": "Indonesia", "philippines": "Philippines", "south africa": "South Africa",
	"mexico": "Mexico", "argentina": "Argentina", "chile": "Chile",
	"colombia": "Colombia", "peru": "Peru", "ukraine": "Ukraine", "greece": "Greece",
	"portugal": "Portugal", "ireland": "Ireland", "czech": "Czechia",
	"hungary": "Hungary", "croatia": "Croatia", "serbia": "Serbia",
	"israel": "Israel", "saudi": "Saudi Arabia", "qatar": "Qatar",
	"kuwait": "Kuwait", "bahrain": "Bahrain", "oman": "Oman", "jordan": "Jordan",
	"lebanon": "Lebanon", "egypt": "Egypt", "nigeria": "Nigeria", "kenya": "Kenya",
	"morocco": "Morocco", "tunisia": "Tunisia", "albania": "Albania",
	"georgia": "Georgia", "armenia": "Armenia", "azerbaijan": "Azerbaijan",
	"kazakhstan": "Kazakhstan", "uzbekistan": "Uzbekistan", "mongolia": "Mongolia",
	"nepal": "Nepal", "sri lanka": "Sri Lanka", "bangladesh": "Bangladesh",
	"pakistan": "Pakistan", "cambodia": "Cambodia", "laos": "Laos",
	"myanmar": "Myanmar", "new zealand": "New Zealand", "iran": "Iran",
	"uae": "UAE", "emirates": "UAE", "luxembourg": "Luxembourg",
	"belgium": "Belgium", "iceland": "Iceland",
}

func countryFromRemark(remark string) string {
	// Flag emoji: two regional-indicator runes U+1F1E6..U+1F1FF.
	runes := []rune(remark)
	for i := 0; i+1 < len(runes); i++ {
		a, b := runes[i], runes[i+1]
		if a >= 0x1F1E6 && a <= 0x1F1FF && b >= 0x1F1E6 && b <= 0x1F1FF {
			code := string([]rune{'A' + (a - 0x1F1E6), 'A' + (b - 0x1F1E6)})
			if name, ok := flagToCountry[code]; ok {
				return name
			}
		}
	}
	lower := strings.ToLower(remark)
	for name, country := range nameToCountry {
		if strings.Contains(lower, name) {
			return country
		}
	}
	return "Other"
}
