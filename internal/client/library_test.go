package client

import (
	"testing"
	"time"
)

func TestLibrary_CreateAndIndex(t *testing.T) {
	lib, err := NewLibrary(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Index()) != 0 {
		t.Fatalf("fresh library should be empty")
	}
	a, err := lib.CreateShallow("scan-a", "srv", []string{"1.1.1.1", "2.2.2.2"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := lib.CreateManual("manual-b", []string{"3.3.3.3"})
	if err != nil {
		t.Fatal(err)
	}
	idx := lib.Index()
	if len(idx) != 2 {
		t.Fatalf("index length: %d", len(idx))
	}
	if a.Meta.Kind != KindShallow {
		t.Fatalf("a kind = %s", a.Meta.Kind)
	}
	if b.Meta.Kind != KindManual {
		t.Fatalf("b kind = %s", b.Meta.Kind)
	}
	if a.Meta.Total != 2 {
		t.Fatalf("a total = %d", a.Meta.Total)
	}
	// Manual list pre-marks all IPs as OK so deep scan can score them.
	if b.Meta.OK != 1 {
		t.Fatalf("manual OK = %d", b.Meta.OK)
	}
}

func TestLibrary_GetAndSavePersists(t *testing.T) {
	dir := t.TempDir()
	lib, _ := NewLibrary(dir)
	l, _ := lib.CreateShallow("scan", "srv", []string{"1.1.1.1"})

	// Drop and reload from disk — index + list file should round-trip.
	lib2, err := NewLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	idx := lib2.Index()
	if len(idx) != 1 || idx[0].ID != l.Meta.ID {
		t.Fatalf("index lost: %+v", idx)
	}
	got, err := lib2.Get(l.Meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.IPs["1.1.1.1"]; !ok {
		t.Fatalf("IPs lost on reload")
	}
}

func TestLibrary_Delete(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	l, _ := lib.CreateShallow("doomed", "srv", []string{"1.1.1.1"})
	if err := lib.Delete(l.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Get(l.Meta.ID); err == nil {
		t.Fatal("Get should fail after Delete")
	}
	if len(lib.Index()) != 0 {
		t.Fatal("index should be empty")
	}
}

func TestLibrary_DeleteOlderThan(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	old, _ := lib.CreateShallow("old", "srv", []string{"1.1.1.1"})
	recent, _ := lib.CreateShallow("recent", "srv", []string{"2.2.2.2"})

	// Back-date `old` directly in the in-memory index. Calling Save()
	// here would bump Updated back to now (it always does), so we
	// mutate the index entry and persist via saveIndex() alone.
	backDate := time.Now().UTC().Add(-7 * 24 * time.Hour)
	lib.mu.Lock()
	lib.metas[old.Meta.ID].Updated = backDate
	lib.mu.Unlock()
	if err := lib.saveIndex(); err != nil {
		t.Fatal(err)
	}

	n, err := lib.DeleteOlderThan(time.Now().UTC().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeleteOlderThan removed %d, want 1", n)
	}
	if _, err := lib.Get(old.Meta.ID); err == nil {
		t.Fatal("old list should be gone")
	}
	if _, err := lib.Get(recent.Meta.ID); err != nil {
		t.Fatalf("recent list should survive: %v", err)
	}
}

func TestLibrary_Rescan(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	src, _ := lib.CreateShallow("src", "srv", []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"})
	// Mark 1.1.1.1 OK, 2.2.2.2 fail, leave 3.3.3.3 pending.
	src.MarkResult("1.1.1.1", &Result{IP: "1.1.1.1", Status: StatusOK})
	src.MarkResult("2.2.2.2", &Result{IP: "2.2.2.2", Status: StatusFail, Reason: FailTimeout})
	_ = lib.Save(src)

	all, err := lib.CreateRescan(src.Meta.ID, "rescan-all", "srv", false)
	if err != nil {
		t.Fatal(err)
	}
	if all.Meta.Total != 3 {
		t.Fatalf("rescan all: total %d, want 3", all.Meta.Total)
	}

	ok, err := lib.CreateRescan(src.Meta.ID, "rescan-ok", "srv", true)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Meta.Total != 1 {
		t.Fatalf("rescan ok: total %d, want 1", ok.Meta.Total)
	}
	if _, ok2 := ok.IPs["1.1.1.1"]; !ok2 {
		t.Fatalf("rescan-ok missing the OK IP")
	}
}

func TestLibrary_NextCopyName_BaseFree(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	if got := lib.NextCopyName("local hosts"); got != "local hosts" {
		t.Fatalf("base free: got %q", got)
	}
}

func TestLibrary_NextCopyName_NumberedSuffix(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	if _, err := lib.CreateManual("local hosts", []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	if got := lib.NextCopyName("local hosts"); got != "local hosts (2)" {
		t.Fatalf("first dup: got %q, want %q", got, "local hosts (2)")
	}
	if _, err := lib.CreateManual("local hosts (2)", []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	if got := lib.NextCopyName("local hosts"); got != "local hosts (3)" {
		t.Fatalf("second dup: got %q, want %q", got, "local hosts (3)")
	}
}

func TestLibrary_NextCopyName_StripsLegacyRescanPrefix(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	// Simulates the old chain: rescan-rescan-rescan-local hosts → should
	// collapse to "local hosts".
	if got := lib.NextCopyName("rescan-rescan-rescan-local hosts"); got != "local hosts" {
		t.Fatalf("legacy strip: got %q", got)
	}
}

func TestLibrary_NextCopyName_StripsExistingNumberSuffix(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	if _, err := lib.CreateManual("local hosts", []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	// Seed already carries a suffix — base should be reused, not stacked
	// into "local hosts (3) (2)".
	if got := lib.NextCopyName("local hosts (3)"); got != "local hosts (2)" {
		t.Fatalf("base recompute: got %q", got)
	}
}

func TestLibrary_CreateRescan_UsesNumberedSuffix(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	a, err := lib.CreateShallow("local hosts", "srv", []string{"1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	r1, err := lib.CreateRescan(a.Meta.ID, "", "srv", false)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Meta.Name != "local hosts (2)" {
		t.Fatalf("rescan 1 name: %q", r1.Meta.Name)
	}
	r2, err := lib.CreateRescan(r1.Meta.ID, "", "srv", false)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Meta.Name != "local hosts (3)" {
		t.Fatalf("rescan 2 name: %q", r2.Meta.Name)
	}
}

func TestList_Counters(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	l, _ := lib.CreateShallow("s", "srv", []string{"a", "b", "c"})
	l.MarkResult("a", &Result{IP: "a", Status: StatusOK})
	l.MarkResult("b", &Result{IP: "b", Status: StatusFail, Reason: FailTimeout})
	if l.Meta.OK != 1 || l.Meta.Failed != 1 {
		t.Fatalf("counters: ok=%d failed=%d", l.Meta.OK, l.Meta.Failed)
	}
	// Idempotent: marking the same IP OK again shouldn't double-count.
	l.MarkResult("a", &Result{IP: "a", Status: StatusOK})
	if l.Meta.OK != 1 {
		t.Fatalf("double-count: ok=%d", l.Meta.OK)
	}
}
