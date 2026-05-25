package client

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"
)

// Level1 runs the shallow scan over a List. Each IP gets up to cfg.Retries
// attempts, and retries are DEFERRED — a failed IP goes to the back of
// the dispatcher's queue so other IPs (including freshly-discovered
// subnet neighbours) are tried before we come back to it.
//
// When cfg.SubnetExpand is on, every OK IP enqueues its /mask neighbours
// as fresh work.
func Level1(ctx context.Context, tester *Tester, l *List, cfg ScanCfg, save func()) {
	if cfg.Parallel <= 0 {
		cfg.Parallel = 100
	}
	if cfg.Duplicate <= 0 {
		cfg.Duplicate = 1
	}
	retries := cfg.Retries
	if retries < 1 {
		retries = 1
	}
	mask := cfg.SubnetMask
	if mask <= 0 || mask > 32 {
		mask = 24
	}

	d := newDispatcher()
	// Seed the dispatcher with all pending IPs, preserving prior attempt
	// counts so a resume after pause doesn't reset the retry budget.
	snap := l.Snapshot()
	for _, ip := range l.Pending() {
		prev := 0
		if r := snap.Results[ip]; r != nil {
			prev = r.Attempts
		}
		d.push(workItem{ip: ip, attempts: prev})
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
			case <-ctx.Done():
				d.cancel()
				return
			case <-doneCh:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < cfg.Parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, d, l, tester, cfg, retries, mask)
		}()
	}
	wg.Wait()
	close(doneCh)
	if save != nil {
		save()
	}
}

type workItem struct {
	ip       string
	attempts int
}

func worker(ctx context.Context, d *dispatcher, l *List, tester *Tester, cfg ScanCfg, retries, mask int) {
	for {
		it, ok := d.pop()
		if !ok {
			return
		}
		if ctx.Err() != nil {
			d.done()
			return
		}
		// Cover-traffic noise — occasionally fire a normal lookup
		// before the protocol query so the resolver sees a mix of
		// "looks ordinary" and our weird base32 labels. Tuned LOW
		// (effective rate ~1/(2*NoiseEvery)) so we don't push the
		// resolver into a rate-limit.
		if cfg.NoiseEnabled && shouldNoise(cfg.NoiseEvery) {
			tester.NoiseQueryOnce(ctx, it.ip)
		}
		l.MarkInProgress(it.ip)
		r := runOne(ctx, tester, it.ip, cfg.Duplicate)
		r.Attempts = it.attempts + 1

		switch {
		case r.Status == StatusOK:
			l.MarkResult(it.ip, r)
			if cfg.SubnetExpand {
				for _, n := range expandSubnet(it.ip, mask) {
					if l.AddPending(n, "subnet") {
						d.push(workItem{ip: n, attempts: 0})
					}
				}
			}
		case r.Attempts >= retries:
			l.MarkResult(it.ip, r)
		default:
			l.RecordTransientFail(it.ip, r)
			d.push(workItem{ip: it.ip, attempts: r.Attempts})
		}
		d.done()
	}
}

func runOne(ctx context.Context, t *Tester, ip string, dup int) *Result {
	var lastReason FailReason = FailNetwork
	var bestRTT time.Duration
	for i := 0; i < dup; i++ {
		status, reason, rtt, _ := t.QueryOnce(ctx, ip)
		if status == StatusOK {
			return &Result{IP: ip, Status: StatusOK, RTTMs: rtt.Milliseconds()}
		}
		if rtt > bestRTT {
			bestRTT = rtt
		}
		lastReason = reason
	}
	return &Result{IP: ip, Status: StatusFail, Reason: lastReason, RTTMs: bestRTT.Milliseconds()}
}

func expandSubnet(ip string, mask int) []string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return nil
	}
	v4 := parsed.To4()
	if v4 == nil {
		return nil // IPv6 not in scope for v1
	}
	_, ipnet, err := net.ParseCIDR(v4.String() + "/" + strconv.Itoa(mask))
	if err != nil {
		return nil
	}
	out := make([]string, 0, 1<<(32-mask))
	for cur := ipnet.IP.Mask(ipnet.Mask).To4(); ipnet.Contains(cur); incrIP(cur) {
		s := cur.String()
		if s != ip {
			out = append(out, s)
		}
		if isAllOnes(cur) {
			break
		}
	}
	return out
}

func incrIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] != 0 {
			return
		}
	}
}

func isAllOnes(ip net.IP) bool {
	for _, b := range ip {
		if b != 0xFF {
			return false
		}
	}
	return true
}
