package web

import (
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sartoopjj/thescanner/internal/client"
)

// Server bundles the HTTP layer that drives the scanner client.
type Server struct {
	cfg    *client.Config
	runner *client.Runner
	tmpls  *template.Template
	srv    *http.Server
}

func New(cfg *client.Config, runner *client.Runner) (*Server, error) {
	tmpls, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, runner: runner, tmpls: tmpls}
	mux := http.NewServeMux()
	s.routes(mux)
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

func (s *Server) Run(ctx context.Context, listen string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	log.Printf("web UI listening on http://%s", ln.Addr().String())
	return s.Serve(ctx, ln)
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shCtx)
	}()
	err := s.srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(StaticFS()))))

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/scan", s.handleScanPage)
	mux.HandleFunc("/lists", s.handleListsPage)
	mux.HandleFunc("/list", s.handleListPage) // ?id=<list-id>
	mux.HandleFunc("/config", s.handleConfigPage)
	mux.HandleFunc("/about", s.handleAboutPage)
	mux.HandleFunc("/privacy", s.handlePrivacyPage)

	// JSON API
	mux.HandleFunc("/api/config", s.apiConfig)
	mux.HandleFunc("/api/i18n", s.apiI18n)
	mux.HandleFunc("/api/help", s.apiHelp)
	mux.HandleFunc("/api/scan/status", s.apiScanStatus)
	mux.HandleFunc("/api/version", s.apiVersion)

	// Library
	mux.HandleFunc("/api/lists", s.apiLists)
	mux.HandleFunc("/api/lists/", s.apiListByID)
	mux.HandleFunc("/api/samples", s.apiSamples)
	mux.HandleFunc("/api/samples/", s.apiSampleContent)

	// Live query log (SSE + ring-buffer backfill)
	mux.HandleFunc("/api/log/recent", s.apiLogRecent)
	mux.HandleFunc("/api/log/stream", s.apiLogStream)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// Default landing depends on whether the user has any lists yet.
	if len(s.runner.Library().Index()) > 0 {
		http.Redirect(w, r, "/lists", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/scan", http.StatusFound)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // JSON API, never embedded in HTML
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg, "error_code": classifyErr(msg)})
}

// classifyErr maps known runner / library error messages to stable
// codes the UI can localize via tt("err."+code, fallback). Keep these
// substring patterns in sync with the literal errors returned in
// internal/client/runner.go — a mismatch makes the UI fall back to the
// generic "err.unknown" line and lose the specific message.
func classifyErr(msg string) string {
	switch {
	case strings.Contains(msg, "no OK IPs to deep-scan"):
		return "no_ok_ips"
	case strings.Contains(msg, "server not found in config"):
		return "server_not_found"
	// Runner.StartShallow / StartDeep return literally
	// "another scan is already in progress" when r.cancel != nil.
	case strings.Contains(msg, "another scan is already in progress"),
		strings.Contains(msg, "scan already running"),
		strings.Contains(msg, "already scanning"):
		return "scan_already_running"
	case strings.Contains(msg, "manual lists can only be deep-scanned"):
		return "manual_shallow_rejected"
	case strings.Contains(msg, "nothing running"):
		return "nothing_running"
	case strings.Contains(msg, "not paused"),
		strings.Contains(msg, "already running"):
		return "scan_state_conflict"
	}
	return "unknown"
}

func (s *Server) renderPage(w http.ResponseWriter, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	lang := s.cfg.Snapshot().UI.Language
	if lang != "fa" {
		lang = "en"
	}
	data["Lang"] = lang
	// Inline the translation bundle so the page can render in the right
	// language on the very first paint — no /api/i18n round-trip = no
	// "1 second of English then Persian" flash.
	if b, err := json.Marshal(i18nBundles[lang]); err == nil {
		data["I18nBundleJSON"] = template.JS(b)
	}
	data["I18nChosen"] = s.cfg.Snapshot().UI.Language != ""
	if err := s.tmpls.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

