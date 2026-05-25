package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sartoopjj/thescanner/internal/client"
)

func newTestServer(t *testing.T) (*Server, *client.Config, *client.Runner) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := client.LoadConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = cfg.Update(func(d *client.ConfigData) {
		d.Servers = []client.ServerEntry{{
			Name:    "default",
			Domains: []string{"v.example.com"},
			Token:   "T-token",
		}}
	})
	lib, err := client.NewLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := client.NewRunner(cfg, lib)
	s, err := New(cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	return s, cfg, r
}

func TestAPIConfig_GetPost(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/config", nil))
	if w.Code != 200 {
		t.Fatalf("GET /api/config: %d", w.Code)
	}
	var d client.ConfigData
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.Servers) != 1 {
		t.Fatalf("servers: %+v", d.Servers)
	}

	d.UI.Language = "fa"
	body, _ := json.Marshal(d)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/config", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("POST /api/config: %d %s", w.Code, w.Body.String())
	}
}

func TestAPIConfig_InvalidServer(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)

	body := []byte(`{"servers":[{"name":"","domains":[],"token":""}]}`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/config", bytes.NewReader(body)))
	if w.Code != 400 {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestAPILists_LifecycleEndpoints(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)

	// Empty index.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"lists":null`) && !strings.Contains(w.Body.String(), `"lists":[]`) {
		t.Fatalf("empty index unexpected: %s", w.Body.String())
	}

	// Create a manual list (no server needed).
	body := []byte(`{"kind":"manual","name":"my-trusted","resolvers":"1.1.1.1\n8.8.8.8"}`)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/lists", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var meta client.ListMeta
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Kind != client.KindManual || meta.OK != 2 {
		t.Fatalf("meta: %+v", meta)
	}

	// Fetch single list.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/"+meta.ID, nil))
	if w.Code != 200 {
		t.Fatalf("get list: %d", w.Code)
	}

	// Rename it.
	body = []byte(`{"name":"renamed-list"}`)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/lists/"+meta.ID+"/rename", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}

	// Export TXT (no OK IPs from a shallow scan, but the manual list pre-marks all OK).
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/"+meta.ID+"/export?format=txt&status=ok", nil))
	if w.Code != 200 {
		t.Fatalf("export: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "1.1.1.1") {
		t.Fatalf("export missing IP: %q", w.Body.String())
	}

	// Delete.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/lists/"+meta.ID, nil))
	if w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}

	// Confirm gone.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/"+meta.ID, nil))
	if w.Code != 404 {
		t.Fatalf("expected 404 after delete, got %d", w.Code)
	}
}

func TestAPILists_PaginatedResults(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)

	// Manual list with many IPs.
	var b strings.Builder
	for i := 0; i < 250; i++ {
		b.WriteString("10.0.0.")
		b.WriteString(itoa(i))
		b.WriteString("\n")
	}
	body, _ := json.Marshal(map[string]any{"kind": "manual", "resolvers": b.String()})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/lists", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("create: %d", w.Code)
	}
	var meta client.ListMeta
	_ = json.Unmarshal(w.Body.Bytes(), &meta)

	// Page 1.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/"+meta.ID+"/results?limit=100&offset=0", nil))
	var page1 struct {
		Count   int `json:"count"`
		Limit   int `json:"limit"`
		Offset  int `json:"offset"`
		Results []struct{ IP string } `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if page1.Count != 250 {
		t.Fatalf("count: %d", page1.Count)
	}
	if len(page1.Results) != 100 {
		t.Fatalf("page1 size: %d", len(page1.Results))
	}

	// Page 3 (offset 200): should have 50.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/"+meta.ID+"/results?limit=100&offset=200", nil))
	var page3 struct {
		Results []struct{ IP string } `json:"results"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &page3)
	if len(page3.Results) != 50 {
		t.Fatalf("page3 size: %d", len(page3.Results))
	}
}

func TestAPILists_ErrorBranches(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)

	// Bare ID prefix with no ID → 400.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/", nil))
	if w.Code != 400 {
		t.Fatalf("empty id: %d", w.Code)
	}

	// Unknown ID → 404 (Get failure).
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/does-not-exist", nil))
	if w.Code != 404 {
		t.Fatalf("unknown id: %d %s", w.Code, w.Body.String())
	}

	// Create a list to attack with bad methods + bad actions.
	body, _ := json.Marshal(map[string]any{"kind": "manual", "name": "x", "resolvers": "1.1.1.1"})
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/lists", bytes.NewReader(body)))
	var meta client.ListMeta
	_ = json.Unmarshal(w.Body.Bytes(), &meta)

	// Unknown action under a real ID → 404 from the dispatcher.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/lists/"+meta.ID+"/teleport", nil))
	if w.Code != 404 {
		t.Fatalf("unknown action: %d", w.Code)
	}

	// GET on a POST-only action → 405.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/"+meta.ID+"/start", nil))
	if w.Code != 405 {
		t.Fatalf("GET on start want 405, got %d", w.Code)
	}

	// Rename to empty → 400.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/lists/"+meta.ID+"/rename", bytes.NewReader([]byte(`{"name":""}`))))
	if w.Code != 400 {
		t.Fatalf("rename empty: %d", w.Code)
	}

	// Export with no format → 400.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/lists/"+meta.ID+"/export", nil))
	if w.Code != 400 {
		t.Fatalf("export no format: %d", w.Code)
	}
}

func TestAPILists_BulkDelete(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)

	// Create two manual lists.
	for _, name := range []string{"a", "b"} {
		body, _ := json.Marshal(map[string]any{"kind": "manual", "name": name, "resolvers": "1.1.1.1"})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/lists", bytes.NewReader(body)))
		if w.Code != 201 {
			t.Fatalf("create %s: %d", name, w.Code)
		}
	}
	// Bulk-delete with a future cutoff → both gone.
	future := "2099-01-01T00:00:00Z"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/lists?older_than="+future, nil))
	if w.Code != 200 {
		t.Fatalf("bulk delete: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"deleted":2`) {
		t.Fatalf("expected deleted:2, got %s", w.Body.String())
	}
}

func TestAPII18n(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/i18n?lang=fa", nil))
	if w.Code != 200 {
		t.Fatalf("i18n: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"lang":"fa"`) {
		t.Fatalf("body: %s", w.Body.String())
	}
}

func TestStaticAssetsEmbed(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/static/style.css", nil))
	if w.Code != 200 {
		t.Fatalf("style.css: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--bg") {
		t.Fatalf("style.css content missing")
	}
}

func TestPages_Render(t *testing.T) {
	s, _, _ := newTestServer(t)
	mux := http.NewServeMux()
	s.routes(mux)
	for _, path := range []string{"/scan", "/lists", "/list", "/config", "/about", "/privacy"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "<html") {
			t.Fatalf("%s: no html", path)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 4)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
