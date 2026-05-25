package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

//go:embed admin.html
var adminHTML []byte

// adminLock serialises config.json writes.
var adminLock sync.Mutex

// serveAdminConfig handles GET/POST on <prefix>/config. Auth wrapper
// is the caller's. POST writes the new config to disk AND hot-reloads
// the live tokens / domains / admin token so DNS auth + panel auth
// switch to the new values immediately — no restart needed.
//
// The response distinguishes "applied immediately" (the common case)
// from "needs restart" (only true for fields that change the listening
// socket: listen, stats_listen, tls_cert, tls_key, admin_path).
func (s *Server) serveAdminConfig(w http.ResponseWriter, r *http.Request, auth func(*http.Request) bool) {
	if !auth(r) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		snapshot := *s.liveCfg()
		if s.cfgPath != "" {
			if onDisk, err := LoadConfig(s.cfgPath); err == nil {
				snapshot = *onDisk
			}
		}
		snapshot.Server.AdminToken = ""
		setSecurityHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(snapshot)

	case http.MethodPost:
		if s.cfgPath == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "no config path configured — server is running entirely from CLI flags",
			})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		var incoming Config
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		prev := s.liveCfg()
		// GET redacts the admin_token; the panel UI doesn't round-trip
		// admin_path. Preserve both unless explicitly changed.
		if incoming.Server.AdminToken == "" {
			incoming.Server.AdminToken = prev.Server.AdminToken
		}
		if incoming.Server.AdminPath == "" {
			incoming.Server.AdminPath = prev.Server.AdminPath
		}
		if err := incoming.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := writeConfigAtomically(s.cfgPath, &incoming); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		// Diff for the restart-needed fields BEFORE we mutate state.
		needsRestart := []string{}
		if incoming.Server.Listen != prev.Server.Listen {
			needsRestart = append(needsRestart, "listen")
		}
		if incoming.Server.StatsListen != prev.Server.StatsListen {
			needsRestart = append(needsRestart, "stats_listen")
		}
		if incoming.Server.TLSCert != prev.Server.TLSCert {
			needsRestart = append(needsRestart, "tls_cert")
		}
		if incoming.Server.TLSKey != prev.Server.TLSKey {
			needsRestart = append(needsRestart, "tls_key")
		}

		// Hot-reload everything we can. The remaining fields (listen,
		// TLS) will land on the next process start.
		newTok := incoming.Server.AdminToken
		newPath := incoming.Server.AdminPath
		s.liveAdminToken.Store(&newTok)
		s.liveAdminPath.Store(&newPath)
		s.h.Reload(&incoming)
		s.cfg.Store(&incoming)

		writeJSON(w, http.StatusOK, map[string]any{
			"saved":             true,
			"applied":           true,
			"needs_restart_for": needsRestart,
		})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeConfigAtomically(path string, cfg *Config) error {
	if path == "" {
		return errors.New("empty config path")
	}
	adminLock.Lock()
	defer adminLock.Unlock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dropRoot(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	dropRoot(tmp)
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	dropRoot(path)
	return nil
}
