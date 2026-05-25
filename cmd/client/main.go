package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sartoopjj/thescanner/internal/client"
	"github.com/sartoopjj/thescanner/internal/version"
	"github.com/sartoopjj/thescanner/internal/web"
)

func main() {
	dataDir := flag.String("data-dir", defaultDataDir(), "data directory for config + state")
	listen := flag.String("listen", "", "override UI listen address (e.g. 127.0.0.1:8080)")
	noBrowser := flag.Bool("no-browser", false, "do not auto-open the UI in a browser")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("thescanner-client %s (%s, %s)", version.Version, version.Commit, version.Date)
		return
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}
	cfgPath := filepath.Join(*dataDir, "config.json")
	cfg, err := client.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	lib, err := client.NewLibrary(*dataDir)
	if err != nil {
		log.Fatalf("library: %v", err)
	}
	runner := client.NewRunner(cfg, lib)

	srv, err := web.New(cfg, runner)
	if err != nil {
		log.Fatalf("web: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	addr := *listen
	if addr == "" {
		addr = cfg.Snapshot().UI.Listen
	}

	if !*noBrowser && shouldOpenBrowser() {
		go openBrowser(uiURL(addr))
	}

	if err := srv.Run(ctx, addr); err != nil {
		log.Fatalf("web: %v", err)
	}
}

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "thescanner")
	}
	return "./thescanner-data"
}

// uiURL builds a browser-friendly URL from a "host:port" listen string,
// turning 0.0.0.0 and :: into 127.0.0.1 so the browser doesn't bark.
func uiURL(addr string) string {
	host := "127.0.0.1"
	port := "8080"
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		h, p := addr[:i], addr[i+1:]
		if h != "" && h != "0.0.0.0" && h != "::" && h != "[::]" {
			host = h
		}
		if p != "" {
			port = p
		}
	}
	return "http://" + host + ":" + port + "/"
}

// shouldOpenBrowser skips auto-open when running headlessly (no DISPLAY on
// Linux, SSH session, or systemd service).
func shouldOpenBrowser() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	if os.Getenv("INVOCATION_ID") != "" { // systemd
		return false
	}
	return true
}

// openBrowser hands the URL to the OS's default opener after a short delay
// so the server has time to bind.
func openBrowser(url string) {
	time.Sleep(400 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
