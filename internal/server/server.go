package server

import (
	"context"
	"log"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

type Server struct {
	// cfg is swapped atomically by the panel save handler so concurrent
	// readers (ShareEntries on /data, GET /config, etc.) never see a
	// half-written struct. The boot-time listen/TLS values still come
	// from the initial Config since they back the live listeners.
	cfg     atomic.Pointer[Config]
	cfgPath string
	stats   *Stats
	h       *Handler
	udps    []*dns.Server // one per CPU when SO_REUSEPORT is supported
	tcp     *dns.Server
	httpS   *http.Server

	// Hot-reloadable scalar mirrors of cfg fields the HTTP layer needs
	// on every request. Kept separate so the auth+routing fast paths
	// don't have to dereference a whole Config snapshot.
	liveAdminToken atomic.Pointer[string]
	liveAdminPath  atomic.Pointer[string]
}

// New wires the DNS server + the stats/admin HTTP server.
// cfgPath, non-empty, lets the panel POST persist edits to disk.
func New(cfg *Config, statsPath, cfgPath string) *Server {
	stats := NewStats(statsPath)
	h := NewHandler(cfg, stats)
	s := &Server{cfgPath: cfgPath, stats: stats, h: h}
	s.cfg.Store(cfg)
	tok := cfg.Server.AdminToken
	pth := cfg.Server.AdminPath
	s.liveAdminToken.Store(&tok)
	s.liveAdminPath.Store(&pth)
	s.tcp = &dns.Server{Addr: cfg.Server.Listen, Net: "tcp", Handler: h}
	s.httpS = &http.Server{
		Addr:              cfg.Server.StatsListen,
		Handler:           s.buildHTTPHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	return s
}

// liveCfg returns a pointer to the current Config snapshot. Callers
// must NOT mutate it — to update, build a fresh Config and Store.
func (s *Server) liveCfg() *Config           { return s.cfg.Load() }
func (s *Server) currentAdminToken() string  { return *s.liveAdminToken.Load() }
func (s *Server) currentAdminPath() string   { return *s.liveAdminPath.Load() }

// Run starts N UDP listeners (SO_REUSEPORT load-balanced on Linux/BSD/
// macOS; one elsewhere), the TCP listener, and the admin HTTP server.
// Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	// Listener addresses come from the boot-time config; they don't
	// hot-reload (changing them requires a process restart).
	boot := s.liveCfg()
	n := 1
	if reuseportSupported {
		n = runtime.GOMAXPROCS(0)
		if n < 1 {
			n = 1
		}
	}
	s.udps = make([]*dns.Server, 0, n)
	errCh := make(chan error, n+2)

	for i := 0; i < n; i++ {
		pc, err := reuseportUDP(boot.Server.Listen)
		if err != nil {
			return err
		}
		srv := &dns.Server{PacketConn: pc, Handler: s.h}
		s.udps = append(s.udps, srv)
		go func(idx int) {
			if idx == 0 {
				log.Printf("dns udp listening on %s (×%d %s)",
					boot.Server.Listen, n, reuseportLabel())
			}
			errCh <- srv.ActivateAndServe()
		}(i)
	}

	go func() {
		log.Printf("dns tcp listening on %s", boot.Server.Listen)
		errCh <- s.tcp.ListenAndServe()
	}()
	go func() {
		if boot.Server.TLSCert != "" && boot.Server.TLSKey != "" {
			log.Printf("admin https listening on %s", boot.Server.StatsListen)
			errCh <- s.httpS.ListenAndServeTLS(boot.Server.TLSCert, boot.Server.TLSKey)
		} else {
			log.Printf("admin http listening on %s", boot.Server.StatsListen)
			errCh <- s.httpS.ListenAndServe()
		}
	}()

	dumpT := time.NewTicker(30 * time.Second)
	defer dumpT.Stop()

	for {
		select {
		case <-ctx.Done():
			return s.shutdown()
		case <-dumpT.C:
			if err := s.stats.Dump(); err != nil {
				log.Printf("stats dump: %v", err)
			}
		case err := <-errCh:
			if err != nil && err != http.ErrServerClosed {
				return err
			}
		}
	}
}

func reuseportLabel() string {
	if reuseportSupported {
		return "SO_REUSEPORT"
	}
	return "single-listener"
}

func (s *Server) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, u := range s.udps {
		_ = u.ShutdownContext(ctx)
	}
	_ = s.tcp.ShutdownContext(ctx)
	_ = s.httpS.Shutdown(ctx)
	return s.stats.Dump()
}
