package web

import (
	"net/http"
	"strings"

	"github.com/sartoopjj/thescanner/internal/client"
)

func (s *Server) apiSamples(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"samples": client.SampleLists()})
}

func (s *Server) apiSampleContent(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/samples/")
	if id == "" || strings.ContainsAny(id, "/\\") {
		writeErr(w, http.StatusBadRequest, "invalid sample id")
		return
	}
	data, err := client.SampleContent(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "sample not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}
