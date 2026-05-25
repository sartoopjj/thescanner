package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/sartoopjj/thescanner/internal/protocol"
)

// hwriter implements dns.ResponseWriter for unit tests.
type hwriter struct {
	msg    *dns.Msg
	local  net.Addr
	remote net.Addr
}

func newHWriter() *hwriter {
	return &hwriter{
		local:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53},
		remote: &net.UDPAddr{IP: net.ParseIP("203.0.113.42"), Port: 53000},
	}
}

func (w *hwriter) LocalAddr() net.Addr         { return w.local }
func (w *hwriter) RemoteAddr() net.Addr        { return w.remote }
func (w *hwriter) WriteMsg(m *dns.Msg) error   { w.msg = m; return nil }
func (w *hwriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *hwriter) Close() error                { return nil }
func (w *hwriter) TsigStatus() error           { return nil }
func (w *hwriter) TsigTimersOnly(bool)         {}
func (w *hwriter) Hijack()                     {}

func mkCfg(t *testing.T) *Config {
	t.Helper()
	c := &Config{
		Server: ServerSection{
			Listen:      "127.0.0.1:0",
			StatsListen: "127.0.0.1:0",
			AdminToken:  "admin",
			// Fixed path so tests can hit the panel deterministically.
			// In production Validate() generates a random one.
			AdminPath: "testpanel0000",
		},
		Domains: []Domain{{Name: "v.example.com"}},
		Tokens:  []Token{{Name: "alice", Secret: "alice-secret-xxxxxxx"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return c
}

func mkPlain(t *testing.T, auth protocol.Auth, qlen int, rlen uint16) *protocol.QueryPlaintext {
	t.Helper()
	pad := make([]byte, qlen-auth.HeaderLen())
	nonce, err := protocol.NewQueryNonce()
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	return &protocol.QueryPlaintext{
		Version: protocol.Version1,
		Nonce:   nonce,
		RespLen: rlen,
		Padding: pad,
	}
}

// /<adminPath>/data is the single consolidated admin endpoint. Every
// other path on the server — /healthz, /stats, /admin, /, /robots.txt,
// you name it — must return a bare 404 with no body so a defender
// scanning common admin URLs can't fingerprint us.
func TestAdminData_AuthAndShape(t *testing.T) {
	cfg := mkCfg(t)
	s := NewStats("")
	srv := httptest.NewServer(StatsHTTPHandler(s, cfg, ""))
	defer srv.Close()
	pfx := "/" + cfg.Server.AdminPath

	// Every well-known probe path must be a bare 404 (no body).
	for _, p := range []string{
		"/", "/healthz", "/stats", "/share", "/admin", "/admin/",
		"/admin/data", "/admin/config", "/robots.txt", "/favicon.ico",
	} {
		r, _ := srv.Client().Get(srv.URL + p)
		if r.StatusCode != 404 {
			t.Fatalf("path %s must 404, got %d", p, r.StatusCode)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			t.Fatalf("path %s leaks body %q", p, body)
		}
	}

	// <prefix>/data: unauthenticated → 404 (also stealth).
	r, _ := srv.Client().Get(srv.URL + pfx + "/data")
	if r.StatusCode != 404 {
		t.Fatalf("<prefix>/data unauth want 404, got %d", r.StatusCode)
	}

	// <prefix>/data: with token → 200 + counters + share array.
	r, err := srv.Client().Get(srv.URL + pfx + "/data?admin=admin")
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("<prefix>/data auth: %v %v", err, r)
	}
	var body struct {
		Share  []ShareEntry `json:"share"`
		Totals struct {
			Queries uint64 `json:"queries"`
		} `json:"totals"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Share) != 1 || body.Share[0].Name != "alice" {
		t.Fatalf("share: %+v", body.Share)
	}
	if !strings.HasPrefix(body.Share[0].URI, "thescanner://server?") {
		t.Fatalf("URI shape: %s", body.Share[0].URI)
	}
	if !strings.Contains(body.Share[0].URI, "token=alice-secret-xxxxxxx") {
		t.Fatalf("URI missing token: %s", body.Share[0].URI)
	}
	if !strings.Contains(body.Share[0].URI, "domains=v.example.com") {
		t.Fatalf("URI missing domain: %s", body.Share[0].URI)
	}

	// Authorization: Bearer alternative.
	req, _ := http.NewRequest("GET", srv.URL+pfx+"/data", nil)
	req.Header.Set("Authorization", "Bearer admin")
	r, _ = srv.Client().Do(req)
	if r.StatusCode != 200 {
		t.Fatalf("bearer <prefix>/data: %d", r.StatusCode)
	}
}

func TestAdmin_GetAndSaveRoundtrip(t *testing.T) {
	cfg := mkCfg(t)
	cfgPath := t.TempDir() + "/config.json"
	s := NewStats("")
	srv := httptest.NewServer(StatsHTTPHandler(s, cfg, cfgPath))
	defer srv.Close()
	pfx := "/" + cfg.Server.AdminPath

	// HTML shell is reachable without a token, but only at the prefix.
	r, err := srv.Client().Get(srv.URL + pfx + "/")
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("<prefix>/ shell: %v %v", err, r)
	}

	// Config GET unauth → stealth 404, not a 401 (that would
	// distinguish "endpoint exists" from "endpoint doesn't").
	r, _ = srv.Client().Get(srv.URL + pfx + "/config")
	if r.StatusCode != 404 {
		t.Fatalf("config GET unauth want 404, got %d", r.StatusCode)
	}

	// With auth, returns the config with admin_token redacted.
	r, _ = srv.Client().Get(srv.URL + pfx + "/config?admin=admin")
	if r.StatusCode != 200 {
		t.Fatalf("config GET auth: %d", r.StatusCode)
	}
	var got Config
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Server.AdminToken != "" {
		t.Fatalf("admin_token leaked in GET: %q", got.Server.AdminToken)
	}
	if len(got.Domains) == 0 || len(got.Tokens) == 0 {
		t.Fatalf("config missing pieces: %+v", got)
	}

	// POST with redacted admin_token must NOT wipe the real token —
	// the handler preserves it.
	got.Domains = append(got.Domains, Domain{Name: "added.example.com"})
	body, _ := json.Marshal(got)
	req, _ := http.NewRequest("POST", srv.URL+pfx+"/config?admin=admin", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	r, err = srv.Client().Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("config POST: %v %d", err, r.StatusCode)
	}

	// Verify the file actually landed with the original admin token AND
	// the new domain.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Server.AdminToken != "admin" {
		t.Fatalf("admin_token clobbered: %q", onDisk.Server.AdminToken)
	}
	foundAdded := false
	for _, d := range onDisk.Domains {
		if d.Name == "added.example.com" {
			foundAdded = true
		}
	}
	if !foundAdded {
		t.Fatalf("new domain not persisted: %+v", onDisk.Domains)
	}
}

func TestStatsRecord(t *testing.T) {
	s := NewStats("")
	s.SetTokenNames([]string{"alice", "bob"})
	s.RecordIdx(0)
	s.RecordIdx(0)
	s.RecordIdx(1)
	snap := s.Snapshot()
	if snap.PerToken["alice"] != 2 || snap.PerToken["bob"] != 1 {
		t.Fatalf("per_token: %+v", snap.PerToken)
	}
	if snap.Totals.Queries != 3 {
		t.Fatalf("totals.queries: %d", snap.Totals.Queries)
	}
}

func TestServeDNS_RoundTrip(t *testing.T) {
	cfg := mkCfg(t)
	stats := NewStats("")
	h := NewHandler(cfg, stats)

	tok := []byte(cfg.Tokens[0].Secret)
	q := mkPlain(t, cfg.AuthMode(), 60, 400)
	wire, err := protocol.EncodeQuery(tok, cfg.AuthMode(), q)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	labels, err := protocol.EncodeLabels(wire)
	if err != nil {
		t.Fatalf("labels: %v", err)
	}
	qname := strings.Join(labels, ".") + "." + cfg.Domains[0].Name + "."

	msg := new(dns.Msg)
	msg.SetQuestion(qname, dns.TypeTXT)
	msg.SetEdns0(4096, false)

	w := newHWriter()
	h.ServeDNS(w, msg)
	if w.msg == nil {
		t.Fatal("no response written")
	}
	if w.msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d", w.msg.Rcode)
	}
	if len(w.msg.Answer) == 0 {
		t.Fatal("no answer")
	}
	txt, ok := w.msg.Answer[0].(*dns.TXT)
	if !ok {
		t.Fatalf("answer not TXT: %T", w.msg.Answer[0])
	}
	encoded := strings.Join(txt.Txt, "")
	respCT, err := protocol.DecodeBase32(encoded)
	if err != nil {
		t.Fatalf("response decode: %v", err)
	}
	resp, err := protocol.DecodeResponse(tok, cfg.AuthMode(), q.Nonce, respCT)
	if err != nil {
		t.Fatalf("response decode: %v", err)
	}
	if resp.NonceEcho != q.Nonce {
		t.Fatal("nonce echo mismatch")
	}
}

// TestServeDNS_SuffixMatching exercises the pre-dotted suffix slice
// across apex, subdomain, and other-domain variations. Locks down
// behavior so the suffix optimization can't quietly change it.
func TestServeDNS_SuffixMatching(t *testing.T) {
	cfg := &Config{
		Server: ServerSection{
			Listen: "127.0.0.1:0", StatsListen: "127.0.0.1:0",
			AdminToken: "a", AdminPath: "testpanel0000",
		},
		Domains: []Domain{{Name: "v.example.com"}, {Name: "x.example.com"}},
		Tokens:  []Token{{Name: "alice", Secret: "alice-secret-xxxxxxx"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	h := NewHandler(cfg, NewStats(""))

	// Subdomain query under v.example.com — valid base32 + suffix → not NXDOMAIN.
	tok := []byte(cfg.Tokens[0].Secret)
	q := mkPlain(t, cfg.AuthMode(), 60, 400)
	wire, _ := protocol.EncodeQuery(tok, cfg.AuthMode(), q)
	labels, _ := protocol.EncodeLabels(wire)

	cases := []struct {
		name    string
		qname   string
		wantNX  bool
	}{
		{"subdomain of v", strings.Join(labels, ".") + ".v.example.com.", false},
		{"subdomain of x", strings.Join(labels, ".") + ".x.example.com.", false},
		{"apex of v", "v.example.com.", true},
		{"apex of x", "x.example.com.", true},
		{"unrelated domain", "foo.attacker.com.", true},
		{"subdomain of suffix's parent", "foo.example.com.", true}, // parent .example.com is NOT our suffix
		{"empty label suffix collision", ".v.example.com.", true}, // bogus leading dot
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := new(dns.Msg)
			msg.SetQuestion(tc.qname, dns.TypeTXT)
			w := newHWriter()
			h.ServeDNS(w, msg)
			isNX := w.msg.Rcode == dns.RcodeNameError
			if isNX != tc.wantNX {
				t.Fatalf("rcode=%d nx=%v, want nx=%v", w.msg.Rcode, isNX, tc.wantNX)
			}
		})
	}
}

func TestServeDNS_BadSuffix_NXDOMAIN(t *testing.T) {
	cfg := mkCfg(t)
	h := NewHandler(cfg, NewStats(""))
	msg := new(dns.Msg)
	msg.SetQuestion("anything.not-our-domain.example.", dns.TypeTXT)
	w := newHWriter()
	h.ServeDNS(w, msg)
	if w.msg.Rcode != dns.RcodeNameError {
		t.Fatalf("want NXDOMAIN, got %d", w.msg.Rcode)
	}
}

func TestServeDNS_BadBase32_NXDOMAIN(t *testing.T) {
	cfg := mkCfg(t)
	h := NewHandler(cfg, NewStats(""))
	msg := new(dns.Msg)
	msg.SetQuestion("bad-chars-here-x."+cfg.Domains[0].Name+".", dns.TypeTXT)
	w := newHWriter()
	h.ServeDNS(w, msg)
	if w.msg.Rcode != dns.RcodeNameError {
		t.Fatalf("want NXDOMAIN, got %d", w.msg.Rcode)
	}
}

func TestServeDNS_NonTXT_NXDOMAIN(t *testing.T) {
	cfg := mkCfg(t)
	h := NewHandler(cfg, NewStats(""))
	msg := new(dns.Msg)
	msg.SetQuestion("foo."+cfg.Domains[0].Name+".", dns.TypeA)
	w := newHWriter()
	h.ServeDNS(w, msg)
	if w.msg.Rcode != dns.RcodeNameError {
		t.Fatalf("want NXDOMAIN, got %d", w.msg.Rcode)
	}
}
