package client

import (
	"sort"
	"strconv"
	"sync"
	"testing"
)

func TestDispatcher_FreshPreferredOverRetry(t *testing.T) {
	d := newDispatcher()
	d.push(workItem{ip: "retry-1", attempts: 1})
	d.push(workItem{ip: "fresh-1", attempts: 0})
	d.push(workItem{ip: "fresh-2", attempts: 0})

	// All workers single-threaded for determinism.
	got := make([]string, 0, 3)
	for {
		it, ok := d.pop()
		if !ok {
			break
		}
		got = append(got, it.ip)
		d.done()
	}
	want := []string{"fresh-1", "fresh-2", "retry-1"}
	if len(got) != 3 {
		t.Fatalf("got %d items: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order: got %v, want %v", got, want)
		}
	}
}

func TestDispatcher_CompletesWhenAllIdle(t *testing.T) {
	d := newDispatcher()
	d.push(workItem{ip: "a", attempts: 0})

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, ok := d.pop()
				if !ok {
					return
				}
				d.done()
			}
		}()
	}
	wg.Wait() // Completes if no deadlock.
}

func TestDispatcher_Cancel(t *testing.T) {
	d := newDispatcher()
	done := make(chan struct{})
	go func() {
		_, _ = d.pop()
		close(done)
	}()
	d.cancel()
	<-done
}

func TestExpandSubnet_24(t *testing.T) {
	got := expandSubnet("192.0.2.5", 24)
	if len(got) != 255 { // /24 has 256 IPs, minus the source IP
		t.Fatalf("/24 expand: got %d, want 255", len(got))
	}
	// Spot-check it includes both ends and excludes the source.
	hit := map[string]bool{}
	for _, ip := range got {
		hit[ip] = true
	}
	for _, want := range []string{"192.0.2.0", "192.0.2.1", "192.0.2.255"} {
		if !hit[want] {
			t.Fatalf("/24 expand missing %s", want)
		}
	}
	if hit["192.0.2.5"] {
		t.Fatal("/24 expand should exclude the source IP")
	}
}

func TestExpandSubnet_30(t *testing.T) {
	got := expandSubnet("192.0.2.6", 30) // network: .4 .5 .6 .7
	want := []string{"192.0.2.4", "192.0.2.5", "192.0.2.7"}
	if len(got) != 3 {
		t.Fatalf("/30 expand: got %v, want %v", got, want)
	}
	hit := map[string]bool{}
	for _, ip := range got {
		hit[ip] = true
	}
	for _, w := range want {
		if !hit[w] {
			t.Fatalf("/30 expand missing %s", w)
		}
	}
}

func TestList_AddPendingAndHas(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	l, err := lib.CreateShallow("test", "server", []string{"1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Has("1.1.1.1") {
		t.Fatal("initial IP not tracked")
	}
	if l.Has("2.2.2.2") {
		t.Fatal("unknown IP reported as known")
	}
	if !l.AddPending("2.2.2.2", "subnet") {
		t.Fatal("AddPending should report true for new IP")
	}
	if l.AddPending("2.2.2.2", "subnet") {
		t.Fatal("AddPending should report false on duplicate")
	}
	if !l.Has("2.2.2.2") {
		t.Fatal("AddPending didn't register the IP")
	}
	snap := l.Snapshot()
	if snap.Meta.Total != 2 {
		t.Fatalf("Total = %d, want 2", snap.Meta.Total)
	}
	if snap.Results["2.2.2.2"].Source != "subnet" {
		t.Fatalf("Source not tagged: %+v", snap.Results["2.2.2.2"])
	}
}

func TestList_RecordTransientFail(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	l, _ := lib.CreateShallow("t", "s", []string{"1.1.1.1"})
	l.RecordTransientFail("1.1.1.1", &Result{IP: "1.1.1.1", Reason: FailTimeout, Attempts: 1})
	snap := l.Snapshot()
	r := snap.Results["1.1.1.1"]
	if r.Reason != FailTimeout {
		t.Fatalf("reason: %s", r.Reason)
	}
	if r.Attempts != 1 {
		t.Fatalf("attempts: %d", r.Attempts)
	}
	if snap.Meta.OK != 0 || snap.Meta.Failed != 0 {
		t.Fatalf("transient fail counted as final: ok=%d failed=%d", snap.Meta.OK, snap.Meta.Failed)
	}
}

// Verifies the deferred-retry invariant under concurrency: every IP that
// gets pushed as a retry (attempts > 0) is observed by some worker AFTER
// every fresh item has already been observed.
func TestDispatcher_RetriesInterleavedWithFresh(t *testing.T) {
	d := newDispatcher()
	const nFresh = 40
	for i := 0; i < nFresh; i++ {
		d.push(workItem{ip: "f" + strconv.Itoa(i), attempts: 0})
	}

	type popped struct {
		it  workItem
		seq int64
	}
	var mu sync.Mutex
	var order []popped

	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				it, seq, ok := d.popWithSeq()
				if !ok {
					return
				}
				mu.Lock()
				order = append(order, popped{it, seq})
				mu.Unlock()
				if it.attempts == 0 {
					d.push(workItem{ip: it.ip + "-r", attempts: 1})
				}
				d.done()
			}
		}()
	}
	wg.Wait()

	if len(order) != nFresh*2 {
		t.Fatalf("expected %d total items popped, got %d", nFresh*2, len(order))
	}

	sort.Slice(order, func(i, j int) bool { return order[i].seq < order[j].seq })

	// New behaviour: ~25% of pops come from the retry queue while fresh
	// still has work, so the Failed counter starts moving long before
	// the entire first pass completes on large lists. Verify at least
	// one retry is interleaved before all fresh items are drained.
	firstRetry := -1
	lastFresh := -1
	for i, p := range order {
		if p.it.attempts > 0 && firstRetry < 0 {
			firstRetry = i
		}
		if p.it.attempts == 0 {
			lastFresh = i
		}
	}
	if firstRetry < 0 {
		t.Fatal("expected some retries to be popped")
	}
	if firstRetry > lastFresh {
		t.Fatalf("first retry at idx %d came after last fresh at idx %d — interleave broken",
			firstRetry, lastFresh)
	}
}
