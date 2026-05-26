package client

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Level2 (a.k.a. "deep scan") scores resolvers by hammering each one
// with l2.QueriesPerResolver queries and computing success rate + p95
// RTT + a composite score. Per-resolver queries run sequentially (so
// RTT measurements aren't contaminated by parallel UDP back-pressure);
// global concurrency is l2.Parallel.
//
// For shallow-scan lists, only IPs currently in StatusOK are scored.
// For manual lists, every IP is pre-marked OK so the whole list runs.
func Level2(ctx context.Context, tester *Tester, l2 Level2Cfg, l *List, save func()) {
	if l2.Parallel <= 0 {
		l2.Parallel = 50
	}
	if l2.QueriesPerResolver <= 0 {
		l2.QueriesPerResolver = 100
	}

	// Resume-friendly target selection: when a previous Level2 pass
	// already scored some IPs (paused + resumed scenario), skip them
	// here so we don't re-do the work AND so the "queries remaining"
	// counter is accurate. Targets is just the not-yet-scored OK IPs.
	allTargets := l.OKIPs()
	targets := make([]string, 0, len(allTargets))
	alreadyDone := 0
	l.mu.Lock()
	for _, ip := range allTargets {
		r := l.Results[ip]
		if r != nil && r.L2Total > 0 {
			alreadyDone += r.L2Total // count toward published progress
			continue
		}
		targets = append(targets, ip)
	}
	// Publish the full-list view: total = ALL eligible IPs × per (so the
	// progress bar represents the whole deep scan, not just this resume),
	// done = queries already fired across previously-scored IPs. New
	// queries from this pass add on top via atomic.AddInt64.
	l.Meta.L2QueriesTotal = int64(len(allTargets)) * int64(l2.QueriesPerResolver)
	l.Meta.L2QueriesDone = int64(alreadyDone)
	l.mu.Unlock()

	work := make(chan string, l2.Parallel*2)
	var wg sync.WaitGroup
	for i := 0; i < l2.Parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range work {
				scoreOne(ctx, tester, l, ip, l2.QueriesPerResolver)
			}
		}()
	}

	doneCh := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if save != nil {
					save()
				}
			case <-doneCh:
				return
			}
		}
	}()

	for _, ip := range targets {
		select {
		case <-ctx.Done():
			close(work)
			wg.Wait()
			close(doneCh)
			if save != nil {
				save()
			}
			return
		case work <- ip:
		}
	}
	close(work)
	wg.Wait()
	close(doneCh)
	if save != nil {
		save()
	}
}

func scoreOne(ctx context.Context, t *Tester, l *List, ip string, per int) {
	rtts := make([]time.Duration, 0, per)
	ok := 0
	noiseEnabled := t.Scan.NoiseEnabled
	for i := 0; i < per; i++ {
		// IMPORTANT: `break` inside `select` only escapes the select, not
		// the surrounding for. The old code accidentally relied on it,
		// so Pause didn't stop the per-IP loop early — each worker kept
		// running its full 50/100 queries after cancellation. Use a
		// non-blocking check + return to exit cleanly. Without this fix,
		// Pause takes ~1–5 minutes to actually halt and Resume races
		// with the still-running goroutine.
		select {
		case <-ctx.Done():
			// Record the partial work so resume can decide whether to
			// re-score this IP. Convention: L2Total==0 means "not yet
			// scored"; any value means "fully or partially scored".
			// We DON'T persist partial L2OK because percentile + score
			// would be misleading on a half-run sample.
			return
		default:
		}
		// Sprinkle decoy lookups through the deep-scan loop too — this
		// is the noisiest path otherwise (per/100 sequential same-IP
		// queries with identical-looking labels). Same low-rate
		// randomised cadence as Level 1.
		if noiseEnabled && shouldNoise(t.Scan.NoiseEvery) {
			t.NoiseQueryOnce(ctx, ip)
		}
		status, _, rtt, _ := t.QueryOnce(ctx, ip)
		if status == StatusOK {
			ok++
			rtts = append(rtts, rtt)
		}
		// Tick the global query counter on EVERY query so the UI bar
		// moves continuously. Without this the bar sits at 0/N for
		// minutes (because L2Scored only ticks when a whole IP's 100
		// queries finish). Atomic so 50+ workers don't need the lock.
		atomic.AddInt64(&l.Meta.L2QueriesDone, 1)
	}
	successRate := float64(ok) / float64(per)
	p95 := percentile(rtts, 0.95)

	// Scoring v2:
	//   - Success rate is the PRIMARY signal (0..100 points).
	//   - Latency is a bounded tiebreaker: each 100 ms of p95 costs
	//     1 point, capped at 30. This prevents the old formula's
	//     pathology where a 21/100 IP with ~210 ms p95 scored 0
	//     (21 − 21 = 0). Now it scores ~19 — still ranks above
	//     complete failures, which is what users want when picking
	//     "best of a bad bunch" resolvers.
	//   - Floor of 1 if at least one query succeeded, so the sort
	//     never lumps "works rarely" in with "never worked".
	latencyPenalty := float64(p95.Milliseconds()) / 100.0
	if latencyPenalty > 30 {
		latencyPenalty = 30
	}
	score := successRate*100.0 - latencyPenalty
	if ok > 0 && score < 1 {
		score = 1
	}
	if score < 0 {
		score = 0
	}

	l.mu.Lock()
	r := l.Results[ip]
	if r == nil {
		r = &Result{IP: ip, Status: StatusOK}
		l.Results[ip] = r
	}
	r.L2Total = per
	r.L2OK = ok
	r.L2P95Ms = p95.Milliseconds()
	r.L2Score = score
	r.UpdatedAt = time.Now().UTC()
	l.Meta.L2Scored = countL2Scored(l)
	l.Meta.Updated = time.Now().UTC()
	persisted := *r
	lib := l.lib
	id := l.Meta.ID
	l.mu.Unlock()

	// Append the deep-scan outcome to results.jsonl so it survives
	// process restart and shows up in the table on next reload. Each
	// IP can get multiple lines (one shallow, one deep) — Get() takes
	// the last one as the truth, which is what we want.
	if lib != nil {
		_ = lib.persistResult(id, &persisted)
	}
}

func countL2Scored(l *List) int {
	// Caller must hold l.mu.
	n := 0
	for _, r := range l.Results {
		if r != nil && r.L2Total > 0 {
			n++
		}
	}
	return n
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), d...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)-1) * p)
	return cp[idx]
}
