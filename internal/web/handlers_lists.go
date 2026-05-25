package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sartoopjj/thescanner/internal/client"
)

// ---- index ---------------------------------------------------------------

// GET /api/lists           → index of all lists
// POST /api/lists          → create a new list. Body:
//     { "kind": "shallow"|"manual", "name": "...", "server": "...",
//       "resolvers": "...", "rescan_from": "<srcID>", "rescan_ok_only": bool }
// DELETE /api/lists?older_than=<RFC3339>  → bulk delete
func (s *Server) apiLists(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out := s.runner.Library().Index()
		writeJSON(w, http.StatusOK, map[string]any{
			"lists":     out,
			"active":    s.runner.ActiveListID(),
		})
	case http.MethodPost:
		s.apiCreateList(w, r)
	case http.MethodDelete:
		s.apiBulkDeleteLists(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type createListReq struct {
	Kind         string `json:"kind"`           // "shallow" or "manual"
	Name         string `json:"name,omitempty"`
	Server       string `json:"server,omitempty"`
	Resolvers    string `json:"resolvers,omitempty"`     // raw paste
	RescanFrom   string `json:"rescan_from,omitempty"`   // source list ID
	RescanOKOnly bool   `json:"rescan_ok_only,omitempty"`
	AutoStart    bool   `json:"auto_start,omitempty"`    // start scan immediately
}

func (s *Server) apiCreateList(w http.ResponseWriter, r *http.Request) {
	var req createListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	lib := s.runner.Library()

	var (
		list *client.List
		err  error
	)
	switch {
	case req.RescanFrom != "":
		list, err = lib.CreateRescan(req.RescanFrom, req.Name, req.Server, req.RescanOKOnly)
	case req.Kind == "manual":
		ips := parseResolverList(req.Resolvers)
		if len(ips) == 0 {
			writeErr(w, http.StatusBadRequest, "no IPs found in resolvers field")
			return
		}
		list, err = lib.CreateManual(req.Name, ips)
	default: // shallow
		ips := parseResolverList(req.Resolvers)
		if len(ips) == 0 {
			writeErr(w, http.StatusBadRequest, "no IPs found in resolvers field")
			return
		}
		list, err = lib.CreateShallow(req.Name, req.Server, ips)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.AutoStart && list.Meta.Kind == client.KindShallow {
		if err := s.runner.StartShallow(list.Meta.ID, req.Server); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusCreated, list.Meta)
}

func (s *Server) apiBulkDeleteLists(w http.ResponseWriter, r *http.Request) {
	older := r.URL.Query().Get("older_than")
	if older == "" {
		writeErr(w, http.StatusBadRequest, "?older_than=RFC3339 required")
		return
	}
	t, err := time.Parse(time.RFC3339, older)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad older_than: "+err.Error())
		return
	}
	n, err := s.runner.Library().DeleteOlderThan(t)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// ---- single list ---------------------------------------------------------

// /api/lists/{id}
// /api/lists/{id}/start    (POST) — start a shallow scan
// /api/lists/{id}/deep     (POST) — start the deep scan
// /api/lists/{id}/pause    (POST)
// /api/lists/{id}/resume   (POST)
// /api/lists/{id}/rename   (POST {name})
// /api/lists/{id}/export   (GET ?format=csv|txt)
// /api/lists/{id}/results  (GET ?status=ok|fail&limit=&offset=&q=)
func (s *Server) apiListByID(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/lists/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErr(w, http.StatusBadRequest, "list id required")
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	lib := s.runner.Library()
	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			l, err := lib.Get(id)
			if err != nil {
				writeErr(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, l.Snapshot())
		case http.MethodDelete:
			if err := lib.Delete(id); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
		default:
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "start":
		s.lifecycle(w, r, id, "start")
	case "deep":
		s.lifecycle(w, r, id, "deep")
	case "pause":
		s.lifecycle(w, r, id, "pause")
	case "resume":
		s.lifecycle(w, r, id, "resume")
	case "rename":
		s.apiRenameList(w, r, id)
	case "results":
		s.apiListResults(w, r, id)
	case "export":
		s.apiListExport(w, r, id)
	default:
		writeErr(w, http.StatusNotFound, "unknown action: "+action)
	}
}

type lifecycleReq struct {
	Server string `json:"server,omitempty"`
}

func (s *Server) lifecycle(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req lifecycleReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	var err error
	switch action {
	case "start":
		err = s.runner.StartShallow(id, req.Server)
	case "deep":
		err = s.runner.StartDeep(id, req.Server)
	case "pause":
		err = s.runner.Pause()
	case "resume":
		err = s.runner.Resume(id, req.Server)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action})
}

type renameReq struct{ Name string `json:"name"` }

func (s *Server) apiRenameList(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req renameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is empty")
		return
	}
	if err := s.runner.Library().Rename(id, req.Name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"renamed": id, "name": req.Name})
}

// ---- per-list results (paginated) ----------------------------------------

type resultRow struct {
	IP     string  `json:"ip"`
	Status string  `json:"status"`
	Reason string  `json:"reason,omitempty"`
	// No `omitempty` on the numeric fields below — a sub-millisecond
	// loopback RTT or a true-zero deep p95 is a meaningful 0, not "not
	// measured". The frontend decides whether to render 0 as blank,
	// "<1ms", or the literal "0" based on the row's context (status,
	// l2_total, list kind).
	RTT    int64   `json:"rtt_ms"`
	L2OK   int     `json:"l2_ok"`
	L2Tot  int     `json:"l2_total"`
	L2P95  int64   `json:"l2_p95_ms"`
	L2Sc   float64 `json:"l2_score"`
	Source string  `json:"source,omitempty"`
}

func collectRows(l *client.ListDTO, statusFilter, q string) []resultRow {
	rows := make([]resultRow, 0, len(l.Results))
	for _, r := range l.Results {
		if statusFilter != "" && string(r.Status) != statusFilter {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.IP), q) {
			continue
		}
		rows = append(rows, resultRow{
			IP: r.IP, Status: string(r.Status), Reason: string(r.Reason),
			RTT: r.RTTMs, L2OK: r.L2OK, L2Tot: r.L2Total, L2P95: r.L2P95Ms,
			L2Sc: r.L2Score, Source: r.Source,
		})
	}
	// Sort order — "best resolvers first":
	//   1. status=ok ahead of fail/anything else.
	//   2. Deep-tested rows ahead of non-deep-tested (so a 0-score
	//      deep-tested row still ranks above a never-tested one).
	//   3. Deep-score, highest first.
	//   4. RTT, lowest first (0 = not measured, push down).
	//   5. IP, lexicographic.
	sort.Slice(rows, func(i, j int) bool {
		iOK := rows[i].Status == "ok"
		jOK := rows[j].Status == "ok"
		if iOK != jOK {
			return iOK
		}
		iDeep := rows[i].L2Tot > 0
		jDeep := rows[j].L2Tot > 0
		if iDeep != jDeep {
			return iDeep
		}
		if iDeep && rows[i].L2Sc != rows[j].L2Sc {
			return rows[i].L2Sc > rows[j].L2Sc
		}
		ri, rj := rows[i].RTT, rows[j].RTT
		if (ri == 0) != (rj == 0) {
			return ri != 0
		}
		if ri != rj {
			return ri < rj
		}
		return rows[i].IP < rows[j].IP
	})
	return rows
}

func (s *Server) apiListResults(w http.ResponseWriter, r *http.Request, id string) {
	l, err := s.runner.Library().Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	dto := l.Snapshot()
	statusFilter := strings.ToLower(r.URL.Query().Get("status"))
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	rows := collectRows(&dto, statusFilter, q)

	limit, offset := paginationParams(r)
	total := len(rows)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meta":    dto.Meta,
		"results": rows[offset:end],
		"offset":  offset,
		"limit":   limit,
		"count":   total,
	})
}

func (s *Server) apiListExport(w http.ResponseWriter, r *http.Request, id string) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	l, err := s.runner.Library().Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	dto := l.Snapshot()
	statusFilter := strings.ToLower(r.URL.Query().Get("status"))
	rows := collectRows(&dto, statusFilter, "")

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(dto.Meta.Name)+`.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"ip", "status", "reason", "rtt_ms", "l2_ok", "l2_total", "l2_p95_ms", "l2_score", "source"})
		for _, x := range rows {
			_ = cw.Write([]string{
				x.IP, x.Status, x.Reason,
				fmt.Sprintf("%d", x.RTT),
				fmt.Sprintf("%d", x.L2OK),
				fmt.Sprintf("%d", x.L2Tot),
				fmt.Sprintf("%d", x.L2P95),
				fmt.Sprintf("%.2f", x.L2Sc),
				x.Source,
			})
		}
		cw.Flush()
	case "txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(dto.Meta.Name)+`.txt"`)
		for _, x := range rows {
			if x.Status == "ok" {
				_, _ = w.Write([]byte(x.IP + "\n"))
			}
		}
	default:
		writeErr(w, http.StatusBadRequest, "format must be csv or txt")
	}
}

func safeFilename(s string) string {
	if s == "" {
		return "list"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func paginationParams(r *http.Request) (limit, offset int) {
	limit = 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
