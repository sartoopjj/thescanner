package web

import (
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// handleScanPage / handleListsPage / handleListPage all just render
// the same HTML shell; routing is client-side off the URL.
func (s *Server) handleScanPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "scan.html", map[string]any{"Page": "scan"})
}
func (s *Server) handleListsPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "lists.html", map[string]any{"Page": "lists"})
}
func (s *Server) handleListPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "list.html", map[string]any{"Page": "list"})
}
func (s *Server) handleAboutPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "about.html", map[string]any{"Page": "about"})
}
func (s *Server) handlePrivacyPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "privacy.html", map[string]any{"Page": "privacy"})
}

// apiScanStatus exposes whatever the runner is currently doing — useful
// for the live dashboard at /scan. The per-list state is the canonical
// source; this is a convenience shortcut.
func (s *Server) apiScanStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"active": s.runner.ActiveListID(),
	}
	writeJSON(w, http.StatusOK, out)
}

// ipRE matches IPv4 with an optional /CIDR. We don't anchor — the input
// can be one-per-line, comma-separated, JSON, or any pasted text.
var ipRE = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:/(\d{1,2}))?\b`)

// parseResolverList extracts valid IPv4s (optional /CIDR) from the input
// and expands CIDRs into host ranges. Tolerates messy / multi-format
// input — see issue history for the file-import bug that motivated this.
func parseResolverList(s string) []string {
	var ips []string
	seen := make(map[string]struct{})
	clean := stripLineComments(s)
	for _, m := range ipRE.FindAllStringSubmatch(clean, -1) {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		c, _ := strconv.Atoi(m[3])
		d, _ := strconv.Atoi(m[4])
		if a > 255 || b > 255 || c > 255 || d > 255 {
			continue
		}
		base := strconv.Itoa(a) + "." + strconv.Itoa(b) + "." + strconv.Itoa(c) + "." + strconv.Itoa(d)
		if m[5] == "" {
			if _, ok := seen[base]; !ok {
				seen[base] = struct{}{}
				ips = append(ips, base)
			}
			continue
		}
		cidr, err := strconv.Atoi(m[5])
		if err != nil || cidr < 0 || cidr > 32 {
			continue
		}
		_, ipnet, err := net.ParseCIDR(base + "/" + strconv.Itoa(cidr))
		if err != nil {
			continue
		}
		for ip := ipnet.IP.Mask(ipnet.Mask).To4(); ipnet.Contains(ip); incIPNet(ip) {
			k := ip.String()
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				ips = append(ips, k)
			}
			if isAllOnesIP(ip) {
				break
			}
		}
	}
	return ips
}

func stripLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func incIPNet(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] != 0 {
			break
		}
	}
}

func isAllOnesIP(ip net.IP) bool {
	for _, b := range ip {
		if b != 0xFF {
			return false
		}
	}
	return true
}
