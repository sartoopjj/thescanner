package server

import (
	"strings"
	"testing"
)

func TestShareEntries_OneEntryPerToken(t *testing.T) {
	cfg := &Config{
		Domains: []Domain{{Name: "v.example.com"}, {Name: "x.example.com"}},
		Tokens:  []Token{{Name: "alice", Secret: "S1"}, {Name: "bob", Secret: "S2"}},
	}
	got := cfg.ShareEntries()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for _, e := range got {
		if !strings.HasPrefix(e.URI, "thescanner://server?") {
			t.Fatalf("URI prefix: %q", e.URI)
		}
		if !strings.Contains(e.URI, "domains=v.example.com%2Cx.example.com") {
			t.Fatalf("domains missing: %q", e.URI)
		}
	}
	if got[0].Name != "alice" || !strings.Contains(got[0].URI, "token=S1") {
		t.Fatalf("alice URI: %+v", got[0])
	}
	if got[1].Name != "bob" || !strings.Contains(got[1].URI, "token=S2") {
		t.Fatalf("bob URI: %+v", got[1])
	}
}

func TestShareEntries_NoDomains(t *testing.T) {
	// Edge case: a fresh server with no domains configured yet. The
	// URI should still be valid — clients reject it on import for a
	// missing domain, which is the correct UX.
	cfg := &Config{Tokens: []Token{{Name: "alice", Secret: "S1"}}}
	got := cfg.ShareEntries()
	if len(got) != 1 {
		t.Fatalf("len: %d", len(got))
	}
	if strings.Contains(got[0].URI, "domains=") {
		t.Fatalf("unexpectedly emitted domains=: %s", got[0].URI)
	}
}

func TestConfig_RejectsDuplicates(t *testing.T) {
	cases := []struct {
		name   string
		tokens []Token
		doms   []Domain
		errSub string
	}{
		{
			name:   "dup token name",
			tokens: []Token{{"alice", "s1"}, {"alice", "s2"}},
			doms:   []Domain{{Name: "d.example.com"}},
			errSub: "duplicates tokens[0]",
		},
		{
			name:   "dup token secret",
			tokens: []Token{{"alice", "s1"}, {"bob", "s1"}},
			doms:   []Domain{{Name: "d.example.com"}},
			errSub: "secret duplicates",
		},
		{
			name:   "dup domain",
			tokens: []Token{{"alice", "s1"}},
			doms:   []Domain{{Name: "d.example.com"}, {Name: "D.EXAMPLE.COM"}},
			errSub: "duplicates domains[0]",
		},
		{
			name:   "all distinct",
			tokens: []Token{{"alice", "s1"}, {"bob", "s2"}},
			doms:   []Domain{{Name: "a.example.com"}, {Name: "b.example.com"}},
			errSub: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{
				Server:  ServerSection{AdminToken: "x"},
				Domains: tc.doms,
				Tokens:  tc.tokens,
			}
			err := c.Validate()
			if tc.errSub == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("err: got %v, want substring %q", err, tc.errSub)
			}
		})
	}
}

func TestConfig_AdminPathAutofill(t *testing.T) {
	// Empty → autofilled with 32 hex chars (128 bits).
	c := &Config{
		Server:  ServerSection{AdminToken: "x"},
		Domains: []Domain{{Name: "d.example.com"}},
		Tokens:  []Token{{Name: "a", Secret: "s"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("autofill: %v", err)
	}
	if len(c.Server.AdminPath) != 32 {
		t.Fatalf("autofilled path len: %q", c.Server.AdminPath)
	}
	if !adminPathRE.MatchString(c.Server.AdminPath) {
		t.Fatalf("autofilled path bad shape: %q", c.Server.AdminPath)
	}

	// User-supplied path is normalized.
	c2 := &Config{
		Server:  ServerSection{AdminToken: "x", AdminPath: " /MyPanel123/ "},
		Domains: []Domain{{Name: "d.example.com"}},
		Tokens:  []Token{{Name: "a", Secret: "s"}},
	}
	if err := c2.Validate(); err != nil {
		t.Fatalf("user path: %v", err)
	}
	if c2.Server.AdminPath != "mypanel123" {
		t.Fatalf("normalize: %q", c2.Server.AdminPath)
	}

	// Bad shapes rejected.
	for _, bad := range []string{"a", "short", "has spaces!", "-leading", "trailing-", "UPPER!"} {
		c3 := &Config{
			Server:  ServerSection{AdminToken: "x", AdminPath: bad},
			Domains: []Domain{{Name: "d.example.com"}},
			Tokens:  []Token{{Name: "a", Secret: "s"}},
		}
		if err := c3.Validate(); err == nil {
			t.Fatalf("bad path %q should reject", bad)
		}
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		err   string
	}{
		{
			"no domains",
			Config{Tokens: []Token{{Name: "a", Secret: "s"}}},
			"at least one domain required",
		},
		{
			"no tokens",
			Config{Domains: []Domain{{Name: "d.example.com"}}},
			"at least one token required",
		},
		{
			"domain blank",
			Config{
				Domains: []Domain{{Name: " "}},
				Tokens:  []Token{{Name: "a", Secret: "s"}},
			},
			"is empty",
		},
		{
			"token missing name",
			Config{
				Domains: []Domain{{Name: "d.example.com"}},
				Tokens:  []Token{{Name: "", Secret: "s"}},
			},
			"tokens[0].name",
		},
		{
			"token missing secret",
			Config{
				Domains: []Domain{{Name: "d.example.com"}},
				Tokens:  []Token{{Name: "a", Secret: ""}},
			},
			"tokens[0].secret",
		},
		{
			"happy path fills defaults",
			Config{
				Domains: []Domain{{Name: "D.Example.COM."}},
				Tokens:  []Token{{Name: "a", Secret: "s"}},
			},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.err == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				// Happy path: defaults applied + domain normalized.
				if tc.cfg.Server.Listen == "" {
					t.Fatalf("listen not filled")
				}
				if tc.cfg.Domains[0].Name != "d.example.com" {
					t.Fatalf("domain not normalized: %q", tc.cfg.Domains[0].Name)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("err: got %v, want substring %q", err, tc.err)
			}
		})
	}
}
