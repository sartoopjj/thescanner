package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sartoopjj/thescanner/internal/client"
)

// apiLogRecent returns the ring buffer of the most recent log events
// — used by the UI to seed the live log pane before the SSE stream
// connects. Optionally filtered by ?list=<id>.
func (s *Server) apiLogRecent(w http.ResponseWriter, r *http.Request) {
	listID := r.URL.Query().Get("list")
	events := s.runner.Log().Recent()
	if listID != "" {
		filtered := events[:0]
		for _, e := range events {
			if e.ListID == listID {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// apiLogStream is a Server-Sent Events endpoint that tails the log bus.
// `?list=<id>` filters to events for one list; omit it for everything.
//
// Heartbeat: a no-op `: ping` comment line every 25s so middleboxes and
// browsers don't drop the connection.
func (s *Server) apiLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx response buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	listID := r.URL.Query().Get("list")
	keep := func(e client.LogEvent) bool { return listID == "" || e.ListID == listID }

	// 1) Backfill from the ring buffer so a freshly-opened pane isn't blank.
	for _, e := range s.runner.Log().Recent() {
		if !keep(e) {
			continue
		}
		writeSSE(w, e)
	}
	flusher.Flush()

	// 2) Subscribe and tail.
	ch, cancel := s.runner.Log().Subscribe(128)
	defer cancel()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			if !keep(e) {
				continue
			}
			writeSSE(w, e)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, e client.LogEvent) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}
