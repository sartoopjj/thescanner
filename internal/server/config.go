package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/sartoopjj/thescanner/internal/protocol"
)

// adminPathRE: 8–64 URL-safe chars, alphanumeric anchors.
var adminPathRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{6,62}[a-z0-9]$`)

// RandomAdminPath returns 32 hex chars (128 bits).
func RandomAdminPath() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "needs-regeneration"
	}
	return hex.EncodeToString(b[:])
}

type Config struct {
	Server  ServerSection `json:"server"`
	Domains []Domain      `json:"domains"`
	Tokens  []Token       `json:"tokens"`
}

type ServerSection struct {
	Listen      string `json:"listen"`
	StatsListen string `json:"stats_listen"`
	AdminToken  string `json:"admin_token"`
	// TLS for the admin HTTP server. Both blank → plain HTTP; both
	// set → HTTPS. Half-set is a config error.
	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`
	// Per-install random URL prefix for the panel. Auto-filled by
	// Validate() when empty. Anything outside /<AdminPath>/ → 404.
	AdminPath string `json:"admin_path,omitempty"`
}

type Domain struct {
	Name string `json:"name"`
}

type Token struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

// LoadConfig parses the JSON config at path. Returns os.ErrNotExist verbatim
// when the file is missing — callers may decide to start from an empty config
// and fill it via CLI flags. Validation is NOT done here; call Validate()
// after applying any CLI overrides.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// Validate fills defaults, normalizes domain names, and checks required
// fields. Safe to call multiple times.
func (c *Config) Validate() error {
	if c.Server.Listen == "" {
		c.Server.Listen = "0.0.0.0:53"
	}
	if c.Server.StatsListen == "" {
		c.Server.StatsListen = "0.0.0.0:8053"
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		return errors.New("config: tls_cert and tls_key must both be set or both be empty")
	}
	c.Server.AdminPath = strings.Trim(strings.ToLower(c.Server.AdminPath), "/ ")
	if c.Server.AdminPath == "" {
		c.Server.AdminPath = RandomAdminPath()
	}
	if !adminPathRE.MatchString(c.Server.AdminPath) {
		return errors.New("config: admin_path must be 8–64 chars of [a-z0-9-], start/end alphanumeric")
	}
	if len(c.Domains) == 0 {
		return errors.New("config: at least one domain required")
	}
	if len(c.Tokens) == 0 {
		return errors.New("config: at least one token required")
	}
	for i := range c.Domains {
		c.Domains[i].Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Domains[i].Name)), ".")
		if c.Domains[i].Name == "" {
			return fmt.Errorf("config: domains[%d].name is empty", i)
		}
	}
	seenName := map[string]int{}
	seenSecret := map[string]int{}
	for i := range c.Tokens {
		if c.Tokens[i].Name == "" {
			return fmt.Errorf("config: tokens[%d].name is empty", i)
		}
		if c.Tokens[i].Secret == "" {
			return fmt.Errorf("config: tokens[%d].secret is empty", i)
		}
		if prev, ok := seenName[c.Tokens[i].Name]; ok {
			return fmt.Errorf("config: tokens[%d].name %q duplicates tokens[%d]", i, c.Tokens[i].Name, prev)
		}
		if prev, ok := seenSecret[c.Tokens[i].Secret]; ok {
			return fmt.Errorf("config: tokens[%d] secret duplicates tokens[%d] — distinct users must hold distinct secrets", i, prev)
		}
		seenName[c.Tokens[i].Name] = i
		seenSecret[c.Tokens[i].Secret] = i
	}
	// Same dedup for domains — duplicate suffixes are a config error
	// (matching loop in handler.go would short-circuit on the first).
	seenDom := map[string]int{}
	for i, d := range c.Domains {
		if prev, ok := seenDom[d.Name]; ok {
			return fmt.Errorf("config: domains[%d].name %q duplicates domains[%d]", i, d.Name, prev)
		}
		seenDom[d.Name] = i
	}
	return nil
}

// AuthMode is always AuthShort (v1). Kept as a method so the rest of the
// server doesn't hard-code the constant.
func (c *Config) AuthMode() protocol.Auth { return protocol.AuthShort }

func (c *Config) TokenBytes() [][]byte {
	out := make([][]byte, len(c.Tokens))
	for i, t := range c.Tokens {
		out[i] = []byte(t.Secret)
	}
	return out
}

// ShareEntry is one ready-to-paste handle for a single token: name +
// the thescanner:// URI a client can import directly.
type ShareEntry struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

// ShareEntries renders every token as a thescanner:// URI bound to
// the server's domains.
func (c *Config) ShareEntries() []ShareEntry {
	doms := make([]string, 0, len(c.Domains))
	for _, d := range c.Domains {
		if d.Name != "" {
			doms = append(doms, d.Name)
		}
	}
	joined := strings.Join(doms, ",")
	out := make([]ShareEntry, 0, len(c.Tokens))
	for _, t := range c.Tokens {
		v := url.Values{}
		v.Set("name", t.Name)
		v.Set("token", t.Secret)
		if joined != "" {
			v.Set("domains", joined)
		}
		out = append(out, ShareEntry{
			Name: t.Name,
			URI:  "thescanner://server?" + v.Encode(),
		})
	}
	return out
}
