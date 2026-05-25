package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sartoopjj/thescanner/internal/version"
)

// Stats tracks per-token query counts. Hot path is lock-free:
// RecordIdx() does one atomic Add into a pre-allocated slot. The map
// + mutex only get touched on rare paths (new token name shows up
// after a hot-reload, or when snapshotting).
type Stats struct {
	startedAt time.Time
	dumpPath  string

	totalQueries atomic.Uint64
	invalid      atomic.Uint64

	// Hot path: tokens slice + counts slice, parallel arrays, indexed
	// by the token index the protocol decoder already returns. Both
	// swapped atomically by SetTokenNames() when config reloads.
	perToken atomic.Pointer[tokenCounters]

	// Cold path: counters for names that appeared *before* SetTokenNames
	// was called (e.g. tryLoad from disk). Merged into perToken on
	// first SetTokenNames.
	mu          sync.Mutex
	overflow    map[string]uint64
}

// tokenCounters is the read-mostly slice the hot path reads via an
// atomic pointer load. Each Counts[i] is independently atomic so
// RecordIdx() touches exactly one cache line.
type tokenCounters struct {
	Names  []string
	Counts []atomic.Uint64
}

func NewStats(dumpPath string) *Stats {
	s := &Stats{
		startedAt: time.Now().UTC(),
		dumpPath:  dumpPath,
		overflow:  map[string]uint64{},
	}
	empty := &tokenCounters{}
	s.perToken.Store(empty)
	s.tryLoad()
	return s
}

// SetTokenNames is called at startup and after every config reload.
// Builds a fresh tokenCounters slice for the current name list,
// preserving any counts we've already accumulated.
func (s *Stats) SetTokenNames(names []string) {
	old := s.perToken.Load()
	oldCount := map[string]uint64{}
	for i, n := range old.Names {
		if v := old.Counts[i].Load(); v != 0 {
			oldCount[n] = v
		}
	}

	s.mu.Lock()
	for n, v := range s.overflow {
		oldCount[n] += v
	}
	s.overflow = map[string]uint64{}
	s.mu.Unlock()

	tc := &tokenCounters{
		Names:  append([]string(nil), names...),
		Counts: make([]atomic.Uint64, len(names)),
	}
	for i, n := range names {
		if v, ok := oldCount[n]; ok {
			tc.Counts[i].Store(v)
			delete(oldCount, n)
		}
	}
	// Names that vanished from the config: stash for snapshot visibility.
	if len(oldCount) > 0 {
		s.mu.Lock()
		for n, v := range oldCount {
			s.overflow[n] = v
		}
		s.mu.Unlock()
	}
	s.perToken.Store(tc)
}

func (s *Stats) tryLoad() {
	if s.dumpPath == "" {
		return
	}
	data, err := os.ReadFile(s.dumpPath)
	if err != nil {
		return
	}
	var disk struct {
		PerToken map[string]uint64 `json:"per_token"`
		Totals   struct {
			Queries uint64 `json:"queries"`
			Invalid uint64 `json:"invalid"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		return
	}
	s.totalQueries.Store(disk.Totals.Queries)
	s.invalid.Store(disk.Totals.Invalid)
	// Stash per-token counters; SetTokenNames merges them in.
	if disk.PerToken != nil {
		s.mu.Lock()
		for n, v := range disk.PerToken {
			s.overflow[n] = v
		}
		s.mu.Unlock()
	}
}

// RecordIdx is the hot path — one atomic Add, no contention. idx is
// the token index that protocol.DecodeQuery already returns.
func (s *Stats) RecordIdx(idx int) {
	s.totalQueries.Add(1)
	tc := s.perToken.Load()
	if idx >= 0 && idx < len(tc.Counts) {
		tc.Counts[idx].Add(1)
	}
}

func (s *Stats) Invalid() { s.invalid.Add(1) }

type statsSnapshot struct {
	PerToken map[string]uint64 `json:"per_token"`
	Totals   struct {
		Queries uint64 `json:"queries"`
		Invalid uint64 `json:"invalid"`
		UptimeS int64  `json:"uptime_s"`
	} `json:"totals"`
	Version   string `json:"version"`
	StartedAt string `json:"started_at"`
}

func (s *Stats) Snapshot() statsSnapshot {
	out := statsSnapshot{
		PerToken:  map[string]uint64{},
		Version:   version.Version,
		StartedAt: s.startedAt.Format(time.RFC3339),
	}
	tc := s.perToken.Load()
	for i, n := range tc.Names {
		if v := tc.Counts[i].Load(); v != 0 {
			out.PerToken[n] = v
		}
	}
	s.mu.Lock()
	for n, v := range s.overflow {
		out.PerToken[n] += v
	}
	s.mu.Unlock()
	out.Totals.Queries = s.totalQueries.Load()
	out.Totals.Invalid = s.invalid.Load()
	out.Totals.UptimeS = int64(time.Since(s.startedAt).Seconds())
	return out
}

func (s *Stats) Dump() error {
	if s.dumpPath == "" {
		return nil
	}
	snap := s.Snapshot()
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.dumpPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	dropRoot(tmp)
	if err := os.Rename(tmp, s.dumpPath); err != nil {
		return err
	}
	dropRoot(s.dumpPath)
	return nil
}

// StatsHTTPHandler builds a standalone admin handler around the given
// Stats + Config. Used in tests; production code calls Server.New
// which uses Server.buildHTTPHandler directly.
func StatsHTTPHandler(stats *Stats, cfg *Config, cfgPath string) http.Handler {
	s := &Server{
		cfgPath: cfgPath,
		stats:   stats,
		h:       NewHandler(cfg, stats),
	}
	s.cfg.Store(cfg)
	tok := cfg.Server.AdminToken
	pth := cfg.Server.AdminPath
	s.liveAdminToken.Store(&tok)
	s.liveAdminPath.Store(&pth)
	return s.buildHTTPHandler()
}

// buildHTTPHandler returns the admin-panel handler. Routing reads the
// CURRENT admin_path / admin_token via atomic pointers, so the panel
// can change either of those at runtime and the next request honors
// it without a process restart. Stealth: anything outside the live
// prefix returns a bare 404 (no body, no Content-Type).
//
//   GET  <prefix>          shell
//   GET  <prefix>/data     counters + share
//   GET  <prefix>/config   current config (token redacted)
//   POST <prefix>/config   replace + persist (cfgPath="" disables POST)
func (s *Server) buildHTTPHandler() http.Handler {
	auth := func(r *http.Request) bool {
		want := []byte(s.currentAdminToken())
		if t := r.URL.Query().Get("admin"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), want) == 1 {
				return true
			}
		}
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got := []byte(strings.TrimPrefix(h, "Bearer "))
			if len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1 {
				return true
			}
		}
		return false
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/" + s.currentAdminPath()
		p := r.URL.Path
		if !(p == prefix || strings.HasPrefix(p, prefix+"/")) {
			// Stealth 404.
			w.Header()["Content-Type"] = nil
			w.Header()["X-Content-Type-Options"] = []string{"nosniff"}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Strip the prefix and route on the remainder.
		rest := strings.TrimPrefix(p, prefix)
		switch rest {
		case "", "/":
			setSecurityHeaders(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(adminHTML)
		case "/data":
			if !auth(r) {
				http.NotFound(w, r)
				return
			}
			type withShare struct {
				statsSnapshot
				Share []ShareEntry `json:"share"`
			}
			out := withShare{
				statsSnapshot: s.stats.Snapshot(),
				Share:         s.liveCfg().ShareEntries(),
			}
			setSecurityHeaders(w)
			w.Header().Set("Content-Type", "application/json")
			enc := json.NewEncoder(w)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(out)
		case "/config":
			s.serveAdminConfig(w, r, auth)
		default:
			http.NotFound(w, r)
		}
	})
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy",
		"default-src 'none'; "+
			"script-src 'self' 'unsafe-inline'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"connect-src 'self'; "+
			"img-src 'self' data:; "+
			"form-action 'self'; "+
			"frame-ancestors 'none'; "+
			"base-uri 'none'")
}
