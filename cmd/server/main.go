package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	// automaxprocs reads the container's cgroup CPU limit and sets
	// GOMAXPROCS to match. Without this, in a Docker container with
	// e.g. 0.5 CPU on a 32-core host, Go would spawn 32 P's and get
	// CFS-throttled into bad tail latency. Side-effect import only.
	_ "go.uber.org/automaxprocs"

	"github.com/sartoopjj/thescanner/internal/server"
	"github.com/sartoopjj/thescanner/internal/version"
)

const defaultConfigPath = "/opt/thescanner/config.json"

func main() {
	cfgPath := flag.String("config", defaultConfigPath, "path to JSON config")
	dataDir := flag.String("data-dir", "/opt/thescanner/data", "data directory for stats.json and runtime state")

	// CLI overrides for individual fields — applied on top of the config
	// file. Useful for one-off runs or keeping secrets out of disk.
	listen := flag.String("listen", "", "override server.listen (e.g. 0.0.0.0:5300)")
	statsListen := flag.String("stats-listen", "", "override server.stats_listen (e.g. 0.0.0.0:8053)")
	adminToken := flag.String("admin-token", "", "override server.admin_token")
	tlsCert := flag.String("tls-cert", "", "path to TLS cert (PEM) — enables HTTPS for the admin panel")
	tlsKey := flag.String("tls-key", "", "path to TLS private key (PEM) — required when -tls-cert is set")
	adminPath := flag.String("admin-path", "", "override server.admin_path (URL prefix the panel mounts at — 8–64 [a-z0-9-] chars)")
	domains := flag.String("domain", "", "comma-separated DNS domains (replaces config domains)")
	tokenName := flag.String("token-name", "", "if set with -token-secret, replaces tokens with this single entry")
	tokenSecret := flag.String("token-secret", "", "shared secret to pair with -token-name")

	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("thescanner-server %s (%s, %s)", version.Version, version.Commit, version.Date)
		return
	}

	raiseFDLimit()

	// Detect whether the user explicitly passed -config. If not (and the
	// default file isn't readable), we silently fall back to an empty
	// config and rely on CLI flags. Only an EXPLICIT -config that can't
	// be read is a hard error.
	explicitConfig := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			explicitConfig = true
		}
	})

	var cfg *server.Config
	c, err := server.LoadConfig(*cfgPath)
	switch {
	case err == nil:
		cfg = c
	case explicitConfig:
		log.Fatalf("config: %v", err)
	default:
		// Default path unreachable (missing, no perms, etc.) — proceed
		// with an empty config; flags must supply everything.
		cfg = &server.Config{}
	}

	applyFlagOverrides(cfg, *listen, *statsListen, *adminToken, *tlsCert, *tlsKey, *adminPath, *domains, *tokenName, *tokenSecret)

	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.Server.AdminToken == "" {
		log.Fatalf("config: server.admin_token is required (set in config or pass -admin-token)")
	}

	// Log panel URL (path is a secret — scrub journal once bookmarked).
	scheme := "http"
	if cfg.Server.TLSCert != "" && cfg.Server.TLSKey != "" {
		scheme = "https"
	}
	host := cfg.Server.StatsListen
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "localhost:" + strings.TrimPrefix(host, "0.0.0.0:")
	} else if strings.HasPrefix(host, "[::]:") {
		host = "localhost:" + strings.TrimPrefix(host, "[::]:")
	}
	log.Printf("admin panel: %s://%s/%s/", scheme, host, cfg.Server.AdminPath)

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}
	// If we're running root-via-sudo (e.g. dev port-53), make the data
	// dir owned by the invoking user so they can read stats.json /
	// list files later without sudo. No-op outside the sudo path.
	server.DropRoot(*dataDir)
	statsPath := filepath.Join(*dataDir, "stats.json")

	s := server.New(cfg, statsPath, *cfgPath)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := s.Run(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func applyFlagOverrides(c *server.Config, listen, statsListen, adminToken, tlsCert, tlsKey, adminPath, domains, tokenName, tokenSecret string) {
	if listen != "" {
		c.Server.Listen = listen
	}
	if statsListen != "" {
		c.Server.StatsListen = statsListen
	}
	if adminToken != "" {
		c.Server.AdminToken = adminToken
	}
	if tlsCert != "" {
		c.Server.TLSCert = tlsCert
	}
	if tlsKey != "" {
		c.Server.TLSKey = tlsKey
	}
	if adminPath != "" {
		c.Server.AdminPath = adminPath
	}
	if domains != "" {
		c.Domains = c.Domains[:0]
		for _, d := range strings.Split(domains, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				c.Domains = append(c.Domains, server.Domain{Name: d})
			}
		}
	}
	if tokenName != "" && tokenSecret != "" {
		c.Tokens = []server.Token{{Name: tokenName, Secret: tokenSecret}}
	}
}
