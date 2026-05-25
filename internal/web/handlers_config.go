package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sartoopjj/thescanner/internal/client"
)

func (s *Server) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "config.html", map[string]any{"Page": "config"})
}

// apiConfig: GET returns current config; POST replaces it.
func (s *Server) apiConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.cfg.Snapshot())
	case http.MethodPost:
		var incoming client.ConfigData
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateConfig(&incoming); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.cfg.Update(func(d *client.ConfigData) { *d = incoming }); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.cfg.Snapshot())
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func validateConfig(d *client.ConfigData) error {
	// Drop totally-empty server rows (user clicked "Add" then didn't fill
	// anything in). This is a normal state when the UI is incrementally
	// edited — don't punish the user with an error on save.
	kept := d.Servers[:0]
	for _, s := range d.Servers {
		nonEmptyDomains := 0
		for _, dom := range s.Domains {
			if strings.TrimSpace(dom) != "" {
				nonEmptyDomains++
			}
		}
		if s.Name == "" && nonEmptyDomains == 0 && s.Token == "" {
			continue
		}
		kept = append(kept, s)
	}
	d.Servers = kept

	for i, s := range d.Servers {
		if s.Name == "" {
			return fmt.Errorf("servers[%d].name is empty", i)
		}
		nonEmpty := 0
		for j := range s.Domains {
			s.Domains[j] = strings.TrimSpace(s.Domains[j])
			if s.Domains[j] != "" {
				nonEmpty++
			}
		}
		if nonEmpty == 0 {
			return fmt.Errorf("servers[%d] has no domains", i)
		}
		if s.Token == "" {
			return fmt.Errorf("servers[%d].token is empty", i)
		}
	}
	sc := d.Scan
	if sc.MinQuery > sc.MaxQuery {
		return fmt.Errorf("scan.min_query > max_query")
	}
	if sc.MinResponse > sc.MaxResponse {
		return fmt.Errorf("scan.min_response > max_response")
	}
	if sc.Parallel < 1 {
		return fmt.Errorf("scan.parallel must be >= 1")
	}
	if sc.Retries < 1 {
		return fmt.Errorf("scan.retries must be >= 1")
	}
	if sc.SubnetMask < 16 || sc.SubnetMask > 32 {
		return fmt.Errorf("scan.subnet_mask must be 16..32")
	}
	if sc.NoiseEnabled && sc.NoiseEvery < 1 {
		return fmt.Errorf("scan.noise_every must be >= 1 when noise is enabled")
	}
	// Without EDNS0 the response can't exceed the classic 512-byte cap.
	// Subtracting DNS framing leaves ~480 bytes of payload. We clamp
	// here rather than at scan time so the user sees the value they'll
	// actually get instead of one silently capped later.
	if !sc.EDNS0 && sc.MaxResponse > 480 {
		return fmt.Errorf("scan.max_response > 480 requires EDNS0 (without EDNS0 the wire cap is 512 bytes)")
	}
	return nil
}
