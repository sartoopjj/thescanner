package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ConfigData is the JSON-serialized shape of the client config.
type ConfigData struct {
	Servers []ServerEntry `json:"servers"`
	Scan    ScanCfg       `json:"scan"`
	Level2  Level2Cfg     `json:"level2"`
	UI      UICfg         `json:"ui"`
}

// ServerEntry points at one scanner-server endpoint. A server can serve
// many DNS suffixes (Domains); the active Tester rotates through them
// per query. The rotation cursor lives on the Tester, not here, so that
// ServerEntry stays plain-value (copyable / vet-clean / JSON-stable).
type ServerEntry struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	Token   string   `json:"token"`
}

type ScanCfg struct {
	MinQuery       int  `json:"min_query"`
	MaxQuery       int  `json:"max_query"`
	MinResponse    int  `json:"min_response"`
	MaxResponse    int  `json:"max_response"`
	EDNS0          bool `json:"edns0"`
	Parallel       int  `json:"parallel"`
	Duplicate      int  `json:"duplicate"`
	TimeoutSeconds int  `json:"timeout_seconds"`
	Retries        int  `json:"retries"`
	SubnetExpand   bool `json:"subnet_expand"`
	SubnetMask     int  `json:"subnet_mask"`

	// Cover traffic: sprinkle a few lookups to well-known public domains
	// among the real protocol queries. When on, the worker fires ONE
	// decoy lookup roughly every NoiseEvery real items — but each
	// occurrence is randomised (uniform 1/NoiseEvery, plus a coin-flip
	// on top) so the cadence isn't periodic. Goal: blend the traffic
	// shape, NOT double the upstream's request rate. Higher NoiseEvery
	// = less noise. Default 30 (≈3% extra queries).
	NoiseEnabled bool `json:"noise_enabled"`
	NoiseEvery   int  `json:"noise_every"`

	// Random "look human" pauses. Every `Every` queries the scan has
	// fired globally (across all workers), ALL workers stop for a
	// random duration in [Min, Max] ms before continuing. Off-by-
	// default would be invisible; ON-by-default means stealth out of
	// the box and users explicitly opt out when they want raw speed.
	RandomPauseEnabled bool `json:"random_pause_enabled"`
	RandomPauseEvery   int  `json:"random_pause_every"`
	RandomPauseMinMs   int  `json:"random_pause_min_ms"`
	RandomPauseMaxMs   int  `json:"random_pause_max_ms"`
}

type Level2Cfg struct {
	QueriesPerResolver int `json:"queries_per_resolver"`
	Parallel           int `json:"parallel"`
}

type UICfg struct {
	Listen   string `json:"listen"`
	Language string `json:"language"`
	// Theme: "", "auto", "light", or "dark". Empty/auto follow the
	// operating-system colour-scheme preference.
	Theme string `json:"theme,omitempty"`
}

// Config is the runtime config holder. The UI mutates it via Update(); the
// scanner reads via Snapshot().
type Config struct {
	mu   sync.RWMutex
	data ConfigData
	path string
}

func DefaultData() ConfigData {
	return ConfigData{
		Scan: ScanCfg{
			// Tuned 2026-05-25 from real-world feedback: shorter padded
			// responses (smaller bandwidth), more parallelism, fewer
			// duplicates+retries so a 100k-IP shallow scan finishes in
			// minutes instead of hours.
			MinQuery: 30, MaxQuery: 50,
			MinResponse: 200, MaxResponse: 500,
			EDNS0: true, Parallel: 1000, Duplicate: 1,
			TimeoutSeconds: 5,
			Retries:        2,
			SubnetExpand:   false,
			SubnetMask:     28,
			NoiseEnabled:   true,
			NoiseEvery:     500,
			// Random global pauses: every ~10k attempts pause all
			// workers 5–15s. Defaults ON so users get stealthy timing
			// out of the box; disable in Config if you're scanning
			// trusted DNS and want raw speed.
			RandomPauseEnabled: true,
			RandomPauseEvery:   20000,
			RandomPauseMinMs:   3000,
			RandomPauseMaxMs:   10000,
		},
		Level2: Level2Cfg{QueriesPerResolver: 50, Parallel: 300},
		// Language deliberately blank — the UI shows a first-run picker
		// when it sees an empty value.
		UI: UICfg{Listen: "127.0.0.1:8080", Language: ""},
	}
}

func LoadConfig(path string) (*Config, error) {
	c := &Config{data: DefaultData(), path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &c.data); err != nil {
		// Accept legacy single-domain entries by retrying with a shim.
		if mig, ok := migrateLegacy(raw); ok {
			c.data = mig
			c.fillRandomPauseDefaults()
			return c, nil
		}
		return nil, err
	}
	c.fillRandomPauseDefaults()
	// Forward-compat: if Domains is empty but legacy "domain" was present
	// in the raw JSON, migrate.
	if needs, mig, ok := maybeMigrate(raw, c.data); needs && ok {
		c.data = mig
	}
	return c, nil
}

// fillRandomPauseDefaults applies sensible RandomPause* values to any
// loaded config where they're missing (zeros). Without this, a config
// written by an older build that didn't have the fields would override
// DefaultData's enabled-by-default with disabled+zeros, leaving the
// stealth pauses silently off until the user re-saves Config.
func (c *Config) fillRandomPauseDefaults() {
	d := DefaultData().Scan
	s := &c.data.Scan
	if s.RandomPauseEvery <= 0 {
		s.RandomPauseEnabled = d.RandomPauseEnabled
		s.RandomPauseEvery = d.RandomPauseEvery
		s.RandomPauseMinMs = d.RandomPauseMinMs
		s.RandomPauseMaxMs = d.RandomPauseMaxMs
	}
}

// maybeMigrate looks at raw JSON for any servers that have a single "domain"
// instead of "domains" and folds them into c.data.Servers[i].Domains.
func maybeMigrate(raw []byte, current ConfigData) (needs bool, out ConfigData, ok bool) {
	var legacy struct {
		Servers []struct {
			Name    string   `json:"name"`
			Domain  string   `json:"domain"`
			Domains []string `json:"domains"`
			Token   string   `json:"token"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return false, current, false
	}
	migrated := false
	for i, s := range legacy.Servers {
		if i >= len(current.Servers) {
			break
		}
		if len(current.Servers[i].Domains) == 0 && s.Domain != "" {
			current.Servers[i].Domains = []string{s.Domain}
			migrated = true
		}
	}
	return migrated, current, true
}

// migrateLegacy is a full fallback for very old configs we can't parse into
// the current shape at all.
func migrateLegacy(raw []byte) (ConfigData, bool) {
	out := DefaultData()
	var legacy struct {
		Servers []struct {
			Name   string `json:"name"`
			Domain string `json:"domain"`
			Token  string `json:"token"`
		} `json:"servers"`
		Scan   ScanCfg   `json:"scan"`
		Level2 Level2Cfg `json:"level2"`
		UI     UICfg     `json:"ui"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return out, false
	}
	for _, s := range legacy.Servers {
		e := ServerEntry{Name: s.Name, Token: s.Token}
		if s.Domain != "" {
			e.Domains = []string{s.Domain}
		}
		out.Servers = append(out.Servers, e)
	}
	if legacy.Scan.MinQuery > 0 {
		out.Scan = legacy.Scan
	}
	if legacy.Level2.QueriesPerResolver > 0 {
		out.Level2 = legacy.Level2
	}
	if legacy.UI.Listen != "" {
		out.UI = legacy.UI
	}
	return out, true
}

func (c *Config) Path() string { return c.path }

func (c *Config) Snapshot() ConfigData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := c.data
	// Deep-copy servers so callers can iterate concurrently with Update().
	out.Servers = make([]ServerEntry, len(c.data.Servers))
	for i, s := range c.data.Servers {
		copyS := ServerEntry{
			Name:    s.Name,
			Token:   s.Token,
			Domains: append([]string(nil), s.Domains...),
		}
		out.Servers[i] = copyS
	}
	return out
}

func (c *Config) Update(fn func(d *ConfigData)) error {
	c.mu.Lock()
	fn(&c.data)
	data, err := json.MarshalIndent(c.data, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// NormalizedDomains returns the server's domains lower-cased with any
// trailing dot stripped.
func (s ServerEntry) NormalizedDomains() []string {
	out := make([]string, 0, len(s.Domains))
	for _, d := range s.Domains {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// PickDomainAt returns the i-th domain in round-robin order. Pure
// function — callers manage the counter (the Tester holds it). Empty
// string when no domains are configured.
func (s ServerEntry) PickDomainAt(i uint64) string {
	doms := s.NormalizedDomains()
	if len(doms) == 0 {
		return ""
	}
	return doms[int(i%uint64(len(doms)))]
}
