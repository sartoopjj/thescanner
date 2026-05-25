package client

import (
	"context"
	"sort"
	"sync"
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

	targets := l.OKIPs()
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
		select {
		case <-ctx.Done():
			break
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
	}
	successRate := float64(ok) / float64(per)
	p95 := percentile(rtts, 0.95)
	score := successRate*100.0 - float64(p95.Milliseconds())/10.0
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
	l.mu.Unlock()
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
