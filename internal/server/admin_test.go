package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdminHTMLEmbedded is the canary for the //go:embed directive in
// admin.go. If somebody deletes/renames admin.html, the package will
// still compile (the directive is happy as long as a matching pattern
// exists), but adminHTML will be empty. This test fails loudly in that
// case.
//
// More importantly: any time `go test ./...` runs, this file's compile
// step exercises the embed pattern itself — so a missing admin.html
// surfaces as a build error before this assertion even runs.
func TestAdminHTMLEmbedded(t *testing.T) {
	if len(adminHTML) == 0 {
		t.Fatal("admin.html embed is empty — check //go:embed and file presence")
	}
	// Sanity: the sign-in form must be in the embedded asset.
	if !bytes.Contains(adminHTML, []byte(`id="signin-card"`)) {
		t.Fatalf("admin.html missing sign-in card (first 120 bytes: %q)", adminHTML[:min(120, len(adminHTML))])
	}
	// Stealth invariant: the pre-auth HTML must NOT advertise the
	// product name. Any string that says "thescanner" in the served
	// HTML breaks the no-brand-pre-auth promise. We do allow the share
	// URI scheme ("thescanner://") in the post-auth JS *function body*,
	// but that's appended dynamically — we render the admin shell via
	// innerHTML after auth — so the embedded file shouldn't carry the
	// scheme as a static string either.
	for _, banned := range []string{"thescanner admin", "<title>thescanner"} {
		if bytes.Contains(bytes.ToLower(adminHTML), []byte(banned)) {
			t.Fatalf("admin.html leaks brand %q before auth", banned)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestAdmin_HTMLShellServed(t *testing.T) {
	cfg := mkCfg(t)
	srv := httptest.NewServer(StatsHTTPHandler(NewStats(""), cfg, ""))
	defer srv.Close()
	pfx := "/" + cfg.Server.AdminPath

	// No auth needed for the HTML shell — the page itself runs the
	// auth UX. We just want a 200 with HTML, served at <prefix>/.
	r, err := srv.Client().Get(srv.URL + pfx + "/")
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("<prefix>/ : %v %v", err, r)
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: %q", ct)
	}
	// Defensive headers must be present on the shell.
	for k, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := r.Header.Get(k); got != want {
			t.Fatalf("header %s: got %q, want %q", k, got, want)
		}
	}
	if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP missing frame-ancestors: %q", csp)
	}

	// Pull the body too and re-check the stealth invariant on the
	// wire — closes the gap between "the embed is clean" (covered by
	// TestAdminHTMLEmbedded) and "what we actually serve is clean".
	body := make([]byte, 64*1024)
	n, _ := r.Body.Read(body)
	got := strings.ToLower(string(body[:n]))
	for _, banned := range []string{"thescanner admin", "<title>thescanner"} {
		if strings.Contains(got, banned) {
			t.Fatalf("served shell leaks brand %q", banned)
		}
	}

	// Deeper paths under the prefix that aren't endpoints must 404.
	r, _ = srv.Client().Get(srv.URL + pfx + "/random-garbage")
	if r.StatusCode != 404 {
		t.Fatalf("<prefix>/random-garbage want 404, got %d", r.StatusCode)
	}
}

// Save → reload → next request honors the new token + new domain
// without restarting the process. Locks down the hot-reload contract.
func TestAdmin_SaveHotReloads(t *testing.T) {
	cfg := mkCfg(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	good, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(StatsHTTPHandler(NewStats(""), cfg, cfgPath))
	defer srv.Close()
	pfx := "/" + cfg.Server.AdminPath

	// Add a new token + domain via panel.
	updated := *cfg
	updated.Tokens = []Token{
		{Name: "alice", Secret: "alice-secret-xxxxxxx"},
		{Name: "bob", Secret: "bob-secret-yyyyyyy"},
	}
	updated.Domains = []Domain{
		{Name: "v.example.com"},
		{Name: "w.example.com"},
	}
	body, _ := json.Marshal(updated)
	req, _ := http.NewRequest("POST", srv.URL+pfx+"/config?admin=admin", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r, _ := srv.Client().Do(req)
	if r.StatusCode != 200 {
		buf, _ := io.ReadAll(r.Body)
		t.Fatalf("save: %d %s", r.StatusCode, buf)
	}
	var saveResp struct {
		Saved           bool     `json:"saved"`
		Applied         bool     `json:"applied"`
		NeedsRestartFor []string `json:"needs_restart_for"`
	}
	if err := json.NewDecoder(r.Body).Decode(&saveResp); err != nil {
		t.Fatal(err)
	}
	if !saveResp.Saved || !saveResp.Applied {
		t.Fatalf("save response: %+v", saveResp)
	}
	if len(saveResp.NeedsRestartFor) != 0 {
		t.Fatalf("unexpected restart-required fields: %v", saveResp.NeedsRestartFor)
	}

	// New panel GET must reflect the new tokens (proves the live cfg
	// swap landed without restart).
	r, _ = srv.Client().Get(srv.URL + pfx + "/config?admin=admin")
	if r.StatusCode != 200 {
		t.Fatalf("config GET after save: %d", r.StatusCode)
	}
	var got Config
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tokens) != 2 || got.Tokens[1].Name != "bob" {
		t.Fatalf("tokens not hot-reloaded: %+v", got.Tokens)
	}
	if len(got.Domains) != 2 || got.Domains[1].Name != "w.example.com" {
		t.Fatalf("domains not hot-reloaded: %+v", got.Domains)
	}

	// Save again, this time changing a listener field — response MUST
	// flag it under needs_restart_for.
	updated.Server.Listen = "127.0.0.1:9999"
	updated.Server.TLSCert = "/some/cert.pem"
	updated.Server.TLSKey = "/some/key.pem"
	body, _ = json.Marshal(updated)
	req, _ = http.NewRequest("POST", srv.URL+pfx+"/config?admin=admin", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r, _ = srv.Client().Do(req)
	if r.StatusCode != 200 {
		buf, _ := io.ReadAll(r.Body)
		t.Fatalf("save2: %d %s", r.StatusCode, buf)
	}
	_ = json.NewDecoder(r.Body).Decode(&saveResp)
	want := map[string]bool{"listen": true, "tls_cert": true, "tls_key": true}
	for _, k := range saveResp.NeedsRestartFor {
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatalf("missing restart flags: %v (got %v)", want, saveResp.NeedsRestartFor)
	}
}

// Verify the timing-safe token check: even a length-mismatched bogus
// token gets the same response shape as a correct-length wrong token.
// (We can't measure timing in a unit test, but we can at least verify
// neither path leaks via different status codes.)
func TestAdmin_BadTokenUniformResponse(t *testing.T) {
	cfg := mkCfg(t)
	srv := httptest.NewServer(StatsHTTPHandler(NewStats(""), cfg, ""))
	defer srv.Close()
	pfx := "/" + cfg.Server.AdminPath
	for _, tok := range []string{"", "x", "wrong", strings.Repeat("a", 4096)} {
		r, _ := srv.Client().Get(srv.URL + pfx + "/data?admin=" + tok)
		if r.StatusCode != 404 {
			t.Fatalf("bad-token %q want 404, got %d", tok, r.StatusCode)
		}
	}
}

// TestConfig_TLSPairValidation: setting only one half of TLS cert/key
// is a config error — easy to mis-configure via the admin panel and
// then wonder why HTTPS won't start.
func TestConfig_TLSPairValidation(t *testing.T) {
	cases := []struct {
		name        string
		cert, key   string
		wantErrSub  string
	}{
		{"both empty", "", "", ""},
		{"both set", "/x/cert", "/x/key", ""},
		{"cert only", "/x/cert", "", "both be set or both be empty"},
		{"key only", "", "/x/key", "both be set or both be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{
				Server: ServerSection{
					AdminToken: "x",
					TLSCert:    tc.cert,
					TLSKey:     tc.key,
				},
				Domains: []Domain{{Name: "d.example.com"}},
				Tokens:  []Token{{Name: "a", Secret: "s"}},
			}
			err := c.Validate()
			if tc.wantErrSub == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("err: got %v, want substring %q", err, tc.wantErrSub)
			}
		})
	}
}

// POST <prefix>/config refuses when cfgPath is empty (server has
// nowhere to persist to).
func TestAdmin_ConfigPOST_RefusesWhenNoCfgPath(t *testing.T) {
	cfg := mkCfg(t)
	srv := httptest.NewServer(StatsHTTPHandler(NewStats(""), cfg, ""))
	defer srv.Close()
	pfx := "/" + cfg.Server.AdminPath

	body, _ := json.Marshal(cfg)
	req, _ := http.NewRequest("POST", srv.URL+pfx+"/config?admin=admin", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 400 {
		t.Fatalf("want 400, got %d", r.StatusCode)
	}
	var got map[string]string
	_ = json.NewDecoder(r.Body).Decode(&got)
	if got["error"] == "" {
		t.Fatalf("expected error body, got %+v", got)
	}
}

// POST <prefix>/config: invalid payload → 400 + config.json untouched.
func TestAdmin_ConfigPOST_ValidationFailureDoesntTouchDisk(t *testing.T) {
	cfg := mkCfg(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	good, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, good, 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(StatsHTTPHandler(NewStats(""), cfg, cfgPath))
	defer srv.Close()
	pfx := "/" + cfg.Server.AdminPath

	bad := *cfg
	bad.Domains = nil
	body, _ := json.Marshal(bad)
	req, _ := http.NewRequest("POST", srv.URL+pfx+"/config?admin=admin", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r, _ := srv.Client().Do(req)
	if r.StatusCode != 400 {
		t.Fatalf("want 400, got %d", r.StatusCode)
	}
	on, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(on, good) {
		t.Fatalf("config.json mutated despite validation failure")
	}
}
