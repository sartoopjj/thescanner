package client

import (
	"bufio"
	"net"
	"os"
	"strings"
)

// LoadResolvers reads a one-IP-per-line file (with `#` comments and CIDR
// expansion). Used by callers who already have a file on disk; the web
// UI passes pasted text through web.parseResolverList instead.
func LoadResolvers(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var ips []string
	seen := make(map[string]struct{})
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "/") {
			_, ipnet, err := net.ParseCIDR(line)
			if err != nil {
				continue
			}
			for ip := ipnet.IP.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
				s := ip.String()
				if _, ok := seen[s]; !ok {
					seen[s] = struct{}{}
					ips = append(ips, s)
				}
			}
			continue
		}
		if net.ParseIP(line) != nil {
			if _, ok := seen[line]; !ok {
				seen[line] = struct{}{}
				ips = append(ips, line)
			}
		}
	}
	return ips, sc.Err()
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] != 0 {
			break
		}
	}
}
