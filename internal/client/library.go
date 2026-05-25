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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	Created  time.Time  `json:"created"`
	Updated  time.Time  `json:"updated"`
}

// List is the full record: metadata + per-IP status + per-IP results.
type List struct {
	Meta    ListMeta              `json:"meta"`
	IPs     map[string]Status     `json:"ips"`     // pending / in_progress / ok / fail
	Results map[string]*Result    `json:"results"` // populated for every IP
	mu      sync.Mutex            `json:"-"`
}

// Library is the in-memory index over the on-disk store.
type Library struct {
	dir   string
	mu    sync.RWMutex
	metas map[string]*ListMeta // id → meta
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
func (lib *Library) listPath(id string) string { return filepath.Join(lib.listsDir(), id+".json") }

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

// Get loads a list from disk. Cheap to call repeatedly; we don't keep
// every list in RAM.
func (lib *Library) Get(id string) (*List, error) {
	data, err := os.ReadFile(lib.listPath(id))
	if err != nil {
		return nil, err
	}
	var l List
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if l.IPs == nil {
		l.IPs = map[string]Status{}
	}
	if l.Results == nil {
		l.Results = map[string]*Result{}
	}
	return &l, nil
}

// Save persists a list and refreshes its index entry.
func (lib *Library) Save(l *List) error {
	l.Meta.Updated = time.Now().UTC()
	// Snapshot under the list mutex so concurrent counters stay coherent.
	l.mu.Lock()
	data, err := json.MarshalIndent(l, "", "  ")
	meta := l.Meta
	l.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := lib.listPath(meta.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, lib.listPath(meta.ID)); err != nil {
		return err
	}
	lib.mu.Lock()
	lib.metas[meta.ID] = &meta
	lib.mu.Unlock()
	return lib.saveIndex()
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
		Results: make(map[string]*Result, len(ips)),
	}
	for _, ip := range ips {
		l.IPs[ip] = StatusPending
		l.Results[ip] = &Result{IP: ip, Status: StatusPending, Source: "initial"}
	}
	if err := lib.Save(l); err != nil {
		return nil, err
	}
	return l, nil
}

// CreateManual creates a manual list — IPs the user already trusts. All
// IPs are pre-marked OK so a deep scan can score them.
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
	}
	for _, ip := range ips {
		l.IPs[ip] = StatusOK
		l.Results[ip] = &Result{IP: ip, Status: StatusOK, Source: "manual"}
	}
	if err := lib.Save(l); err != nil {
		return nil, err
	}
	return l, nil
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
	_ = os.Remove(lib.listPath(id))
	return lib.saveIndex()
}

// DeleteOlderThan removes every list whose Updated is before `before`.
// Returns the number deleted.
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
	for _, id := range victims {
		_ = os.Remove(lib.listPath(id))
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

// MarkResult records a final per-IP outcome and adjusts list-level counters.
func (l *List) MarkResult(ip string, r *Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := l.IPs[ip]
	r.UpdatedAt = time.Now().UTC()
	l.Results[ip] = r
	l.IPs[ip] = r.Status
	switch {
	case r.Status == StatusOK && prev != StatusOK:
		l.Meta.OK++
	case r.Status == StatusFail && prev != StatusFail && prev != StatusOK:
		l.Meta.Failed++
	}
	l.Meta.Updated = time.Now().UTC()
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
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.IPs[ip]
	return ok
}

// AddPending inserts a new pending IP. Returns true when a new entry
// was created. `source` is recorded on the Result for filtering.
func (l *List) AddPending(ip, source string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.IPs[ip]; ok {
		return false
	}
	l.IPs[ip] = StatusPending
	l.Results[ip] = &Result{IP: ip, Status: StatusPending, Source: source}
	l.Meta.Total++
	return true
}

// Pending returns IPs still needing shallow-scan work, sorted.
func (l *List) Pending() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0)
	for ip, st := range l.IPs {
		if st == StatusPending || st == StatusInProgress {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out
}

// OKIPs returns IPs currently in StatusOK (used by deep scan).
func (l *List) OKIPs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
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
// callers (API responses). The original is not retained.
func (l *List) Snapshot() ListDTO {
	l.mu.Lock()
	defer l.mu.Unlock()
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
