// Package mobile is the gomobile entry point for thescanner-client on
// Android (and iOS, if we ever add that target). gomobile wraps each
// exported function below as a method on the generated Java class.
//
// Constraints to remember when editing this file:
//   - Only types in the gomobile bind subset can cross the boundary:
//     string, int (= int32 on Android), int64, float64, bool, []byte,
//     error, plus structs/interfaces declared in this package.
//   - No goroutines visible to the host language; long-running work
//     stays inside Go.
//   - Methods on exported structs are exposed; package-level funcs are
//     exposed as static methods of the wrapper class.
//
// Usage from Kotlin (Android wrapper):
//
//   val app = Mobile.newApp()
//   app.start("/data/data/com.example.thescanner/files", "127.0.0.1:39000")
//   val url = app.address()    // e.g. "http://127.0.0.1:39000/"
//   webView.loadUrl(url)
//   ...
//   app.stop()
//
// Build the AAR with:
//   gomobile bind -target=android -o android/app/libs/scanner.aar \
//     -androidapi 21 \
//     github.com/sartoopjj/thescanner/mobile
package mobile

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"

	"github.com/sartoopjj/thescanner/internal/client"
	"github.com/sartoopjj/thescanner/internal/web"
)

// App is the gomobile-friendly handle held by the host wrapper (Android
// Activity or iOS ServerController). One App = one running embedded HTTP
// server.
type App struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	addr   string
}

// NewApp constructs a fresh App handle. (gomobile cannot expose a global,
// so the host language constructs one and keeps it for the activity's
// lifetime.)
func NewApp() *App { return &App{} }

// Start brings up the embedded UI server and the scanner runner.
//
//	dataDir  app-private storage path (Android: Context.filesDir.absolutePath)
//	listen   bind address; pass "127.0.0.1:0" to let the OS pick a free port —
//	         then read the actual URL via App.URL()
//
// Idempotent: a second Start() while already running returns an error.
func (a *App) Start(dataDir, listen string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return errors.New("already running")
	}

	cfgPath := filepath.Join(dataDir, "config.json")
	cfg, err := client.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	lib, err := client.NewLibrary(dataDir)
	if err != nil {
		return err
	}
	runner := client.NewRunner(cfg, lib)

	srv, err := web.New(cfg, runner)
	if err != nil {
		return err
	}

	// Bind first so we can hand the real port back to the WebView before
	// returning. Use a context cancellable from Stop().
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	a.addr = "http://" + ln.Addr().String() + "/"

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go func() { _ = srv.Serve(ctx, ln) }()
	return nil
}

// Address returns the bound URL (set by Start). Empty if Start hasn't been
// called or is between Stop() and the next Start(). Named Address rather
// than URL so the gomobile-generated Swift binding produces address()
// instead of the awkward uRL().
func (a *App) Address() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.addr
}

// Stop terminates the embedded server. Safe to call when not running.
func (a *App) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
		a.addr = ""
	}
}

// Running reports whether the embedded server is up.
func (a *App) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancel != nil
}
