package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResolvers_CIDRAndComments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ips.txt")
	body := `
# comment
8.8.8.8

1.1.1.1
192.0.2.0/30
not-an-ip
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ips, err := LoadResolvers(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"8.8.8.8", "1.1.1.1", "192.0.2.0", "192.0.2.1", "192.0.2.2", "192.0.2.3"}
	if len(ips) != len(want) {
		t.Fatalf("ips: got %v, want %v", ips, want)
	}
	for i, w := range want {
		if ips[i] != w {
			t.Fatalf("ips[%d]=%s want %s", i, ips[i], w)
		}
	}
}

func TestConfigSaveLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Update(func(d *ConfigData) {
		d.Servers = append(d.Servers, ServerEntry{
			Name:    "default",
			Domains: []string{"v.example.com", "x.example.com"},
			Token:   "T",
		})
		d.UI.Language = "fa"
	}); err != nil {
		t.Fatal(err)
	}
	c2, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	s := c2.Snapshot()
	if len(s.Servers) != 1 || s.Servers[0].Name != "default" {
		t.Fatalf("servers: %+v", s.Servers)
	}
	if s.UI.Language != "fa" {
		t.Fatalf("lang: %s", s.UI.Language)
	}
}

func TestServerEntry_NormalizedDomains(t *testing.T) {
	s := ServerEntry{Domains: []string{"V.Example.COM.", "x.example.com", "FOO.BAR.", " ", ""}}
	want := []string{"v.example.com", "x.example.com", "foo.bar"}
	got := s.NormalizedDomains()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] %s ≠ %s", i, got[i], want[i])
		}
	}
}

func TestServerEntry_PickDomainAt(t *testing.T) {
	s := ServerEntry{Domains: []string{"a.example.com", "b.example.com", "c.example.com"}}
	want := []string{"a.example.com", "b.example.com", "c.example.com", "a.example.com", "b.example.com"}
	for i, w := range want {
		got := s.PickDomainAt(uint64(i))
		if got != w {
			t.Fatalf("pick #%d: got %s, want %s", i, got, w)
		}
	}
}

func TestTester_RotatesDomains(t *testing.T) {
	tester := NewTester(
		ServerEntry{Domains: []string{"a.example.com", "b.example.com", "c.example.com"}},
		ScanCfg{},
	)
	want := []string{"a.example.com", "b.example.com", "c.example.com", "a.example.com"}
	for i, w := range want {
		got := tester.pickDomain()
		if got != w {
			t.Fatalf("pick #%d: got %s, want %s", i, got, w)
		}
	}
}
