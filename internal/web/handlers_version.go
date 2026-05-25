package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/sartoopjj/thescanner/internal/version"
)

// /api/version: current build + latest GitHub release tag (6h cache).
// UI shows a banner when latest is ahead. No auto-update.
type versionResp struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	UpToDate  bool   `json:"up_to_date"`
	CheckedAt int64  `json:"checked_at"`
}

var (
	verMu        sync.Mutex
	verCache     versionResp
	verCacheTime time.Time
)

const (
	verCacheTTL = 6 * time.Hour
	verRepoAPI  = "https://api.github.com/repos/sartoopjj/thescanner/releases/latest"
	verRepoWeb  = "https://github.com/sartoopjj/thescanner/releases/latest"
)

func (s *Server) apiVersion(w http.ResponseWriter, r *http.Request) {
	verMu.Lock()
	if time.Since(verCacheTime) < verCacheTTL && verCache.Current != "" {
		resp := verCache
		verMu.Unlock()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	verMu.Unlock()

	resp := versionResp{
		Current:   version.Version,
		URL:       verRepoWeb,
		CheckedAt: time.Now().Unix(),
	}
	if latest, ok := fetchLatestTag(r.Context()); ok {
		resp.Latest = latest
		resp.UpToDate = !isNewer(latest, version.Version)
	} else {
		resp.UpToDate = true // fetch failed → UI hides banner
	}

	verMu.Lock()
	verCache = resp
	verCacheTime = time.Now()
	verMu.Unlock()

	writeJSON(w, http.StatusOK, resp)
}

func fetchLatestTag(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", verRepoAPI, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", false
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false
	}
	return body.TagName, body.TagName != ""
}

// isNewer: latest > current after stripping leading "v". "dev"/"unknown"
// treated as up-to-date so dev builds don't nag.
func isNewer(latest, current string) bool {
	if current == "" || current == "dev" || current == "unknown" {
		return false
	}
	l := trimV(latest)
	c := trimV(current)
	if l == c {
		return false
	}
	return semverLess(c, l)
}

func trimV(s string) string {
	if len(s) > 0 && (s[0] == 'v' || s[0] == 'V') {
		return s[1:]
	}
	return s
}

// semverLess: numeric major.minor.patch; ignores pre-release suffixes.
func semverLess(a, b string) bool {
	ap := splitParts(a)
	bp := splitParts(b)
	for i := 0; i < 3; i++ {
		if ap[i] != bp[i] {
			return ap[i] < bp[i]
		}
	}
	return false
}

func splitParts(s string) [3]int {
	var out [3]int
	var cur, idx int
	for i := 0; i < len(s) && idx < 3; i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			cur = cur*10 + int(c-'0')
		case c == '.':
			out[idx] = cur
			idx++
			cur = 0
		default:
			out[idx] = cur
			return out
		}
	}
	if idx < 3 {
		out[idx] = cur
	}
	return out
}
