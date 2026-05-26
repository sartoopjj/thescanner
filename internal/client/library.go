// Package client's Library tracks every scan ever started, persisted across
// runs. Each entry in the Library is a List — a named collection of IPs
// with their results. Two kinds: KindShallow (created by a Start), and
// KindManual (created by the user, IPs are pre-marked OK so a deep scan
// can score them directly).
//
// On disk:
//
//	<data-dir>/
//	  lists.json              — index: array of ListMeta (sorted newest first)
//	  lists/<id>.json         — full per-list payload (status + results)
package client

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type ListKind string

const (
	KindShallow ListKind = "shallow"
	KindManual  ListKind = "manual"
)

type ListStatus string

const (
	ListPending  ListStatus = "pending"   // newly created, not started
	ListScanning ListStatus = "scanning"  // shallow scan running
	ListPaused   ListStatus = "paused"    // user paused
	ListDone     ListStatus = "done"      // shallow scan finished
	ListDeep     ListStatus = "deep"      // deep scan running
	ListDeepDone ListStatus = "deep_done" // deep scan finished
)

// ListMeta is the index entry — small, cheap to load all of them.
type ListMeta struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Kind     ListKind   `json:"kind"`
	Status   ListStatus `json:"status"`
	Server   string     `json:"server,omitempty"`
	Total    int        `json:"total"`
	OK       int        `json:"ok"`
	Failed   int        `json:"failed"`
	L2Scored int        `json:"l2_scored,omitempty"`
	// Query-level deep-scan progress, in addition to per-IP L2Scored.
	// L2Scored only ticks when an IP's entire `per` query budget has
	// been spent (so the UI bar stays at 0/N for many minutes when
	// QueriesPerResolver=100); these counters move on every single
	// query so users see live activity. Updated via atomic.AddInt64
	// from Level2 workers, so plain int64 type is intentional.
	L2QueriesDone  int64     `json:"l2_queries_done,omitempty"`
	L2QueriesTotal int64     `json:"l2_queries_total,omitempty"`
	// Attempted is incremented (atomically) every time a worker finishes
	// runOne — including transient failures that go back to the retry
	// queue. Lets the UI show real progress while Failed/OK only move
	// on final outcomes (which is slow when retries are deferred).
	Attempted int64     `json:"attempted,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// List is the full record: metadata + per-IP status + per-IP results.
type List struct {
	Meta    ListMeta           `json:"meta"`
	IPs     map[string]Status  `json:"ips"`     // pending / in_progress / ok / fail
	Results map[string]*Result `json:"results"` // populated only after each IP is tested
	mu      sync.RWMutex       `json:"-"`
	// lib back-ref so List.MarkResult / AppendPending can persist
	// incrementally to disk (append a line to results.jsonl, append
	// to ips.txt). Set by Library when it creates or loads a List.
	lib *Library `json:"-"`
}

// Library is the in-memory index over the on-disk store.
type Library struct {
	dir   string
	mu    sync.RWMutex
	metas map[string]*ListMeta // id → meta

	// Small cache so UI polls (every 2s) don't rebuild the whole *List
	// from disk each time — that was allocating ~80 MB per call on a
	// 1M-IP list and pinning the heap at the same value indefinitely.
	cacheMu   sync.Mutex
	cacheID   string
	cacheList *List
}

func NewLibrary(dir string) (*Library, error) {
	if err := os.MkdirAll(filepath.Join(dir, "lists"), 0o700); err != nil {
		return nil, err
	}
	lib := &Library{dir: dir, metas: map[string]*ListMeta{}}
	if err := lib.loadIndex(); err != nil {
		return nil, err
	}
	return lib, nil
}

func (lib *Library) indexPath() string         { return filepath.Join(lib.dir, "lists.json") }
func (lib *Library) listsDir() string          { return filepath.Join(lib.dir, "lists") }
func (lib *Library) listDir(id string) string  { return filepath.Join(lib.listsDir(), id) }
func (lib *Library) metaPath(id string) string { return filepath.Join(lib.listDir(id), "meta.json") }
func (lib *Library) ipsPath(id string) string  { return filepath.Join(lib.listDir(id), "ips.txt") }
func (lib *Library) resultsPath(id string) string {
	return filepath.Join(lib.listDir(id), "results.jsonl")
}
// Successful resolvers go to results-ok.jsonl with full metadata
// (RTT, L2 score, …). Failed IPs go to results-fail.txt as plain
// lines — just the IP, no JSON overhead, no per-IP metadata. For a
// 1M-IP scan with 99% fail rate this cuts the on-disk footprint by
// ~80% versus storing every failure as a JSON line.
func (lib *Library) okResultsPath(id string) string {
	return filepath.Join(lib.listDir(id), "results-ok.jsonl")
}
func (lib *Library) failResultsPath(id string) string {
	return filepath.Join(lib.listDir(id), "results-fail.txt")
}

// Legacy single-file path. Kept only to migrate or delete pre-refactor data.
func (lib *Library) legacyListPath(id string) string {
	return filepath.Join(lib.listsDir(), id+".json")
}

func (lib *Library) loadIndex() error {
	data, err := os.ReadFile(lib.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var arr []ListMeta
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("lists.json: %w", err)
	}
	for i := range arr {
		m := arr[i]
		lib.metas[m.ID] = &m
	}
	return nil
}

func (lib *Library) saveIndex() error {
	lib.mu.RLock()
	arr := make([]ListMeta, 0, len(lib.metas))
	for _, m := range lib.metas {
		arr = append(arr, *m)
	}
	lib.mu.RUnlock()
	sort.Slice(arr, func(i, j int) bool { return arr[i].Updated.After(arr[j].Updated) })
	data, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}
	tmp := lib.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, lib.indexPath())
}

// Index returns a snapshot of every list's metadata, newest first.
func (lib *Library) Index() []ListMeta {
	lib.mu.RLock()
	out := make([]ListMeta, 0, len(lib.metas))
	for _, m := range lib.metas {
		out = append(out, *m)
	}
	lib.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// Get returns the *List for id. Single-slot cache: the most recently
// requested list is held in memory and returned to subsequent callers
// without re-reading disk. Critical for the UI's 2-second polling on
// large lists — without it every poll allocated ~80 MB rebuilding the
// IPs map.
func (lib *Library) Get(id string) (*List, error) {
	lib.cacheMu.Lock()
	if lib.cacheID == id && lib.cacheList != nil {
		l := lib.cacheList
		lib.cacheMu.Unlock()
		return l, nil
	}
	lib.cacheMu.Unlock()

	metaB, err := os.ReadFile(lib.metaPath(id))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	var meta ListMeta
	if err := json.Unmarshal(metaB, &meta); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	l := &List{
		Meta:    meta,
		IPs:     map[string]Status{},
		Results: map[string]*Result{},
		lib:     lib,
	}

	if f, err := os.Open(lib.ipsPath(id)); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			ip := strings.TrimSpace(sc.Text())
			if ip != "" {
				l.IPs[ip] = StatusPending
			}
		}
		f.Close()
	}

	// OK results — full JSON metadata.
	if f, err := os.Open(lib.okResultsPath(id)); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var r Result
			if err := json.Unmarshal(line, &r); err != nil {
				continue
			}
			rc := r
			l.Results[r.IP] = &rc
			if r.Status != "" {
				l.IPs[r.IP] = r.Status
			}
		}
		f.Close()
	}
	// Failed IPs — plain text, one IP per line. We don't allocate a
	// Result entry in memory for these (status is enough); collectRows
	// synthesises a minimal row from the IPs map when the UI asks for
	// fails. Saves ~200 B × N entries on million-IP scans.
	if f, err := os.Open(lib.failResultsPath(id)); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			ip := strings.TrimSpace(sc.Text())
			if ip != "" {
				l.IPs[ip] = StatusFail
			}
		}
		f.Close()
	}
	// Legacy single results.jsonl from the previous storage layout.
	// Read-only; new persistence goes to the split files above.
	if f, err := os.Open(lib.resultsPath(id)); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var r Result
			if err := json.Unmarshal(line, &r); err != nil {
				continue
			}
			if r.Status == StatusOK {
				rc := r
				l.Results[r.IP] = &rc
			}
			if r.Status != "" {
				l.IPs[r.IP] = r.Status
			}
		}
		f.Close()
	}

	if l.Meta.Kind == KindManual {
		for ip := range l.IPs {
			if l.IPs[ip] == StatusPending {
				l.IPs[ip] = StatusOK
			}
		}
	}
	lib.cacheMu.Lock()
	lib.cacheID = id
	lib.cacheList = l
	lib.cacheMu.Unlock()
	return l, nil
}

// Save writes ONLY the meta.json file for this list and refreshes the
// global index. It does NOT rewrite the (potentially huge) IP set or
// the per-result log — those are append-only. So a "save" during scan
// is a few hundred bytes, not 40+ MB.
func (lib *Library) Save(l *List) error {
	l.Meta.Updated = time.Now().UTC()
	if err := os.MkdirAll(lib.listDir(l.Meta.ID), 0o700); err != nil {
		return err
	}
	if err := lib.writeMeta(l); err != nil {
		return err
	}
	meta := l.MetaCopy()
	lib.mu.Lock()
	lib.metas[meta.ID] = &meta
	lib.mu.Unlock()
	return lib.saveIndex()
}

// writeMeta atomically rewrites <id>/meta.json with the current Meta.
func (lib *Library) writeMeta(l *List) error {
	tmp := lib.metaPath(l.Meta.ID) +
		fmt.Sprintf(".%d.%d.tmp", os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	l.mu.RLock()
	encErr := json.NewEncoder(f).Encode(l.Meta)
	l.mu.RUnlock()
	closeErr := f.Close()
	if encErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if encErr != nil {
			return encErr
		}
		return closeErr
	}
	if err := os.Rename(tmp, lib.metaPath(l.Meta.ID)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// appendIPLines appends raw IPs to <id>/ips.txt. Used by CreateXxx and
// subnet expansion. Writes are buffered then flushed; each line is
// "ip\n" so reload via bufio.Scanner is trivial.
func (lib *Library) appendIPLines(id string, ips []string) error {
	if len(ips) == 0 {
		return nil
	}
	if err := os.MkdirAll(lib.listDir(id), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(lib.ipsPath(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriterSize(f, 64*1024)
	for _, ip := range ips {
		if _, err := bw.WriteString(ip); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// persistResult routes the result to the right on-disk file based on
// its status. OK gets a full JSON line in results-ok.jsonl; fail gets
// just an IP+newline in results-fail.txt (no metadata, much smaller).
// POSIX guarantees O_APPEND writes are atomic up to PIPE_BUF (4 KB).
func (lib *Library) persistResult(id string, r *Result) error {
	if r.Status == StatusOK {
		f, err := os.OpenFile(lib.okResultsPath(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		return json.NewEncoder(f).Encode(r)
	}
	if r.Status == StatusFail {
		f, err := os.OpenFile(lib.failResultsPath(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(r.IP + "\n")
		return err
	}
	return nil
}

// CreateShallow creates a new shallow-scan list seeded with the given IPs.
// All IPs start in pending status.
func (lib *Library) CreateShallow(name, server string, ips []string) (*List, error) {
	if name == "" {
		name = "scan-" + time.Now().UTC().Format("2006-01-02-1504")
	}
	id, err := newListID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	l := &List{
		Meta: ListMeta{
			ID: id, Name: name, Kind: KindShallow, Status: ListPending,
			Server: server, Total: len(ips), Created: now, Updated: now,
		},
		IPs:     make(map[string]Status, len(ips)),
		Results: make(map[string]*Result),
		lib:     lib,
	}
	for _, ip := range ips {
		l.IPs[ip] = StatusPending
	}
	if err := lib.appendIPLines(id, ips); err != nil {
		return nil, err
	}
	if err := lib.Save(l); err != nil {
		return nil, err
	}
	return l, nil
}

func (lib *Library) CreateManual(name string, ips []string) (*List, error) {
	if name == "" {
		name = "manual-" + time.Now().UTC().Format("2006-01-02-1504")
	}
	id, err := newListID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	l := &List{
		Meta: ListMeta{
			ID: id, Name: name, Kind: KindManual, Status: ListDone,
			Total: len(ips), OK: len(ips), Created: now, Updated: now,
		},
		IPs:     make(map[string]Status, len(ips)),
		Results: make(map[string]*Result, len(ips)),
		lib:     lib,
	}
	for _, ip := range ips {
		l.IPs[ip] = StatusOK
		l.Results[ip] = &Result{IP: ip, Status: StatusOK, Source: "manual"}
	}
	if err := lib.appendIPLines(id, ips); err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if err := lib.persistResult(id, l.Results[ip]); err != nil {
			return nil, err
		}
	}
	if err := lib.Save(l); err != nil {
		return nil, err
	}
	return l, nil
}

// CreateShallowEmpty + CreateManualEmpty + AppendIPsFromReader form
// the streaming counterpart to CreateShallow / CreateManual. Used by
// the multipart upload path in handlers_lists.go so the server never
// needs to hold the whole IP list as a single string — it appends
// IPs into the list as it reads the multipart body line by line.
// This is what makes million-IP scans feasible on a mobile build.

// CreateShallowEmpty creates a shallow-scan list with no IPs yet.
// Callers MUST follow up with AppendIPsFromReader (or AppendIPs) and
// Save() before kicking off Level1. The empty state is persisted so
// a process crash mid-upload doesn't lose the list ID.
func (lib *Library) CreateShallowEmpty(name, server string) (*List, error) {
	if name == "" {
		name = "scan-" + time.Now().UTC().Format("2006-01-02-1504")
	}
	id, err := newListID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	l := &List{
		Meta: ListMeta{
			ID: id, Name: name, Kind: KindShallow, Status: ListPending,
			Server: server, Created: now, Updated: now,
		},
		IPs:     make(map[string]Status),
		Results: make(map[string]*Result),
		lib:     lib,
	}
	if err := lib.Save(l); err != nil {
		return nil, err
	}
	lib.cacheMu.Lock()
	lib.cacheID = id
	lib.cacheList = l
	lib.cacheMu.Unlock()
	return l, nil
}

func (lib *Library) CreateManualEmpty(name string) (*List, error) {
	if name == "" {
		name = "manual-" + time.Now().UTC().Format("2006-01-02-1504")
	}
	id, err := newListID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	l := &List{
		Meta: ListMeta{
			ID: id, Name: name, Kind: KindManual, Status: ListDone,
			Created: now, Updated: now,
		},
		IPs:     make(map[string]Status),
		Results: make(map[string]*Result),
		lib:     lib,
	}
	if err := lib.Save(l); err != nil {
		return nil, err
	}
	return l, nil
}

// AppendIPsFromReader streams IPs out of `r` (one IP/CIDR per line,
// blanks and # comments ignored) and appends them to `l`. Duplicates
// are de-duped against what's already in l.IPs. CIDR notation is
// expanded inline.
//
// The function holds l.mu only briefly per batch so concurrent readers
// (the UI's /api/lists/{id}/results poll) aren't blocked for the whole
// upload. Save is called ONCE at the end — saving per-line would melt
// the disk on a million-IP list.
//
// Important: Results entries are NOT pre-allocated. They get created
// lazily by Level1 when an IP is tested. For huge lists this saves
// ~100 bytes × N entries up-front and lets us scale to millions of
// IPs without blowing the mobile process's memory budget.
func (lib *Library) AppendIPsFromReader(l *List, r io.Reader) (int, error) {
	const batchSize = 50_000
	added := 0
	batchIPs := make([]string, 0, batchSize)
	batchResults := make([]*Result, 0, batchSize)

	flushBatch := func() error {
		if len(batchIPs) == 0 {
			return nil
		}
		// Append IPs file (always).
		if err := lib.appendIPLines(l.Meta.ID, batchIPs); err != nil {
			return err
		}
		// Manual lists also persist the OK Result lines.
		if l.Meta.Kind == KindManual {
			for _, r := range batchResults {
				if err := lib.persistResult(l.Meta.ID, r); err != nil {
					return err
				}
			}
		}
		batchIPs = batchIPs[:0]
		batchResults = batchResults[:0]
		l.mu.Lock()
		l.Meta.Total = len(l.IPs)
		if l.Meta.Kind == KindManual {
			l.Meta.OK = l.Meta.Total
		}
		l.Meta.Updated = time.Now().UTC()
		l.mu.Unlock()
		return lib.Save(l)
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		expanded := expandLine(line)
		if len(expanded) == 0 {
			continue
		}
		l.mu.Lock()
		for _, ip := range expanded {
			if _, exists := l.IPs[ip]; exists {
				continue
			}
			if l.Meta.Kind == KindManual {
				l.IPs[ip] = StatusOK
				rr := &Result{IP: ip, Status: StatusOK, Source: "manual"}
				l.Results[ip] = rr
				batchResults = append(batchResults, rr)
			} else {
				l.IPs[ip] = StatusPending
			}
			batchIPs = append(batchIPs, ip)
			added++
		}
		l.mu.Unlock()
		if len(batchIPs) >= batchSize {
			if err := flushBatch(); err != nil {
				return added, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return added, err
	}
	if err := flushBatch(); err != nil {
		return added, err
	}
	return added, nil
}

// expandLine parses a single config-style line into the IPs it
// represents. Handles bare "1.2.3.4" and CIDR "1.2.3.0/24" notation,
// strips inline comments. Kept inline (no regex globals, no shared
// state) so it's safe to call from many concurrent appenders without
// locking — bufio.Scanner already gives us one line at a time.
var libIPRE = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:/(\d{1,2}))?\b`)

func expandLine(line string) []string {
	// Drop "# comment" tails.
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = line[:i]
	}
	m := libIPRE.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	a, b, c, d := atoiByte(m[1]), atoiByte(m[2]), atoiByte(m[3]), atoiByte(m[4])
	if a > 255 || b > 255 || c > 255 || d > 255 {
		return nil
	}
	base := fmt.Sprintf("%d.%d.%d.%d", a, b, c, d)
	if m[5] == "" {
		return []string{base}
	}
	cidr := atoiByte(m[5])
	if cidr < 0 || cidr > 32 {
		return nil
	}
	// /32 → single host; bigger CIDRs expand into ranges. We cap the
	// expansion at /8 (16 M hosts) to refuse user typos like a bare
	// "10.0.0.0/0" that would otherwise pin the goroutine forever.
	if cidr < 8 {
		return nil
	}
	out := make([]string, 0, 1<<(32-cidr))
	first := (uint32(a) << 24) | (uint32(b) << 16) | (uint32(c) << 8) | uint32(d)
	mask := uint32(0xFFFFFFFF) << (32 - cidr)
	netAddr := first & mask
	bcast := netAddr | ^mask
	for ip := netAddr; ip <= bcast; ip++ {
		out = append(out, fmt.Sprintf("%d.%d.%d.%d", (ip>>24)&0xFF, (ip>>16)&0xFF, (ip>>8)&0xFF, ip&0xFF))
		if ip == 0xFFFFFFFF {
			break // saturated, avoid wrap
		}
	}
	return out
}

func atoiByte(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// CreateRescan creates a new shallow-scan list seeded from the IPs of
// `srcID`. If okOnly is true, only IPs that were OK in the source list
// are carried over; otherwise every IP comes along.
//
// When the caller doesn't supply a name we generate a Windows-copy-style
// numbered name: "<base>", then "<base> (2)", "<base> (3)" on each
// subsequent rescan. Legacy stacked "rescan-rescan-…" prefixes from the
// previous naming scheme get peeled off so a chain doesn't keep growing.
func (lib *Library) CreateRescan(srcID, name, server string, okOnly bool) (*List, error) {
	src, err := lib.Get(srcID)
	if err != nil {
		return nil, err
	}
	var ips []string
	for ip, st := range src.IPs {
		if okOnly && st != StatusOK {
			continue
		}
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	if name == "" {
		name = lib.NextCopyName(src.Meta.Name)
	}
	return lib.CreateShallow(name, server, ips)
}

// NextCopyName picks "<base>" if free, otherwise the lowest "<base> (N)"
// (N ≥ 2) that isn't already in the library. Visible for tests; called
// internally by CreateRescan when no explicit name is given.
func (lib *Library) NextCopyName(seed string) string {
	base := cleanCopyBase(seed)
	lib.mu.RLock()
	taken := make(map[string]struct{}, len(lib.metas))
	for _, m := range lib.metas {
		taken[m.Name] = struct{}{}
	}
	lib.mu.RUnlock()
	if _, ok := taken[base]; !ok {
		return base
	}
	for n := 2; n < 100000; n++ {
		cand := fmt.Sprintf("%s (%d)", base, n)
		if _, ok := taken[cand]; !ok {
			return cand
		}
	}
	return base + "-" + time.Now().UTC().Format("20060102T150405")
}

var (
	rxCopySuffix   = regexp.MustCompile(`\s*\(\d+\)\s*$`)
	rxRescanPrefix = regexp.MustCompile(`^(?:rescan-)+`)
)

// cleanCopyBase strips both the trailing "(N)" suffix and any leading
// "rescan-" prefixes (the latter cleans up names produced by the
// previous naming scheme).
func cleanCopyBase(s string) string {
	s = rxRescanPrefix.ReplaceAllString(s, "")
	s = rxCopySuffix.ReplaceAllString(s, "")
	return s
}

// Delete removes a list (file + index entry).
func (lib *Library) Delete(id string) error {
	lib.mu.Lock()
	delete(lib.metas, id)
	lib.mu.Unlock()
	lib.cacheMu.Lock()
	if lib.cacheID == id {
		lib.cacheID = ""
		lib.cacheList = nil
	}
	lib.cacheMu.Unlock()
	_ = os.RemoveAll(lib.listDir(id))
	_ = os.Remove(lib.legacyListPath(id))
	return lib.saveIndex()
}

func (lib *Library) DeleteOlderThan(before time.Time) (int, error) {
	lib.mu.Lock()
	var victims []string
	for id, m := range lib.metas {
		if m.Updated.Before(before) {
			victims = append(victims, id)
		}
	}
	for _, id := range victims {
		delete(lib.metas, id)
	}
	lib.mu.Unlock()
	lib.cacheMu.Lock()
	for _, id := range victims {
		if lib.cacheID == id {
			lib.cacheID = ""
			lib.cacheList = nil
			break
		}
	}
	lib.cacheMu.Unlock()
	for _, id := range victims {
		_ = os.RemoveAll(lib.listDir(id))
		_ = os.Remove(lib.legacyListPath(id))
	}
	return len(victims), lib.saveIndex()
}

// Rename changes a list's display name.
func (lib *Library) Rename(id, name string) error {
	l, err := lib.Get(id)
	if err != nil {
		return err
	}
	l.Meta.Name = name
	return lib.Save(l)
}

// ---- per-list mutation helpers (called by Level1/Level2 workers) ----

// MarkInProgress flips a pending IP to in_progress.
func (l *List) MarkInProgress(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.IPs[ip]; !ok {
		return
	}
	l.IPs[ip] = StatusInProgress
	if r := l.Results[ip]; r != nil {
		r.Status = StatusInProgress
	}
}

// MarkResult records a final per-IP outcome. OK results go into the
// in-memory l.Results map (UI needs metadata for the table). Failed
// IPs are NOT stored in l.Results — they only update l.IPs (the
// status enum) and the on-disk results-fail.txt. That saves ~200 B
// per failed IP × millions of entries on big shallow scans.
func (l *List) MarkResult(ip string, r *Result) {
	l.mu.Lock()
	prev := l.IPs[ip]
	r.UpdatedAt = time.Now().UTC()
	if r.Status == StatusOK {
		l.Results[ip] = r
	} else {
		delete(l.Results, ip)
	}
	l.IPs[ip] = r.Status
	switch {
	case r.Status == StatusOK && prev != StatusOK:
		l.Meta.OK++
	case r.Status == StatusFail && prev != StatusFail && prev != StatusOK:
		l.Meta.Failed++
	}
	l.Meta.Updated = time.Now().UTC()
	lib := l.lib
	id := l.Meta.ID
	l.mu.Unlock()
	if lib != nil {
		_ = lib.persistResult(id, r)
	}
}

// RecordTransientFail stores a failed attempt without finalising the
// per-IP status — the dispatcher will retry the IP later.
func (l *List) RecordTransientFail(ip string, attempt *Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.Results[ip]
	if r == nil {
		r = &Result{IP: ip}
		l.Results[ip] = r
	}
	r.Reason = attempt.Reason
	r.RTTMs = attempt.RTTMs
	r.Attempts = attempt.Attempts
	r.UpdatedAt = time.Now().UTC()
	l.IPs[ip] = StatusInProgress
}

// Has reports whether ip is tracked by the list.
func (l *List) Has(ip string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.IPs[ip]
	return ok
}

// AddPending inserts a new pending IP (subnet expansion). Persists
// the new IP to ips.txt so it survives restart.
func (l *List) AddPending(ip, source string) bool {
	l.mu.Lock()
	if _, ok := l.IPs[ip]; ok {
		l.mu.Unlock()
		return false
	}
	l.IPs[ip] = StatusPending
	l.Meta.Total++
	lib := l.lib
	id := l.Meta.ID
	l.mu.Unlock()
	if lib != nil {
		_ = lib.appendIPLines(id, []string{ip})
	}
	return true
}

func (l *List) Pending() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0)
	for ip, st := range l.IPs {
		if st == StatusPending || st == StatusInProgress {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out
}

func (l *List) OKIPs() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0)
	for ip, st := range l.IPs {
		if st == StatusOK {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out
}

// Snapshot returns a deep-enough copy of the list's data for read-only
// callers (API responses). Avoid for hot read paths on big lists — for
// a million-IP list this allocates ~80 MB per call. Use MetaCopy +
// ForEachResult instead.
func (l *List) Snapshot() ListDTO {
	l.mu.RLock()
	defer l.mu.RUnlock()
	dto := ListDTO{
		Meta:    l.Meta,
		Status:  make(map[string]Status, len(l.IPs)),
		Results: make(map[string]*Result, len(l.Results)),
	}
	for k, v := range l.IPs {
		dto.Status[k] = v
	}
	for k, v := range l.Results {
		c := *v
		dto.Results[k] = &c
	}
	return dto
}

func (l *List) MetaCopy() ListMeta {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.Meta
}

// ForEachResult invokes fn for every Result under a read lock. Note
// failed IPs no longer get a Result entry (memory optimisation); use
// ForEachIP if you need to enumerate them.
func (l *List) ForEachResult(fn func(*Result)) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, r := range l.Results {
		if r == nil {
			continue
		}
		fn(r)
	}
}

// ForEachIP yields every tracked IP + its current status under RLock.
// Use this to build fail-row summaries (where no Result is allocated)
// or to enumerate pending IPs.
func (l *List) ForEachIP(fn func(ip string, st Status)) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for ip, st := range l.IPs {
		fn(ip, st)
	}
}

// ListDTO is what API responses serialise (no embedded mutex).
type ListDTO struct {
	Meta    ListMeta           `json:"meta"`
	Status  map[string]Status  `json:"status"`
	Results map[string]*Result `json:"results"`
}

func newListID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:]), nil
}
