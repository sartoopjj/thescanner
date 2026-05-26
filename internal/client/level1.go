package client

import (
	"context"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
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

	throttle := &globalThrottle{}

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

	// Global random-pause throttler. Watches Meta.Attempted and, every
	// RandomPauseEvery queries fired in TOTAL (not per worker), sets a
	// shared no-before deadline. Workers check the deadline before each
	// pop and sleep until it passes. So when the throttler triggers,
	// the entire scan pauses for 5–15s, not just one worker.
	if cfg.RandomPauseEnabled && cfg.RandomPauseEvery > 0 && cfg.RandomPauseMaxMs > 0 {
		go runRandomPauseThrottle(ctx, doneCh, &l.Meta.Attempted, throttle, cfg)
	}

	var wg sync.WaitGroup
	for i := 0; i < cfg.Parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, d, l, tester, cfg, retries, mask, throttle)
		}()
	}
	wg.Wait()
	close(doneCh)
	if save != nil {
		save()
	}
}

// globalThrottle is a single shared deadline used by all workers to
// coordinate the random-pause feature: when a throttler sets noBefore
// in the future, every worker sleeps until that time before pulling
// another item from the dispatcher.
type globalThrottle struct {
	noBefore atomic.Int64
}

func (t *globalThrottle) wait(ctx context.Context) {
	nb := t.noBefore.Load()
	if nb == 0 {
		return
	}
	delay := nb - time.Now().UnixNano()
	if delay <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(delay)):
	}
}

func runRandomPauseThrottle(ctx context.Context, doneCh <-chan struct{},
	attemptedPtr *int64, throttle *globalThrottle, cfg ScanCfg) {
	lo, hi := cfg.RandomPauseMinMs, cfg.RandomPauseMaxMs
	if hi < lo {
		hi = lo
	}
	every := int64(cfg.RandomPauseEvery)
	last := atomic.LoadInt64(attemptedPtr)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-doneCh:
			return
		case <-t.C:
			cur := atomic.LoadInt64(attemptedPtr)
			if cur-last < every {
				continue
			}
			last = cur
			ms := lo
			if span := hi - lo; span > 0 {
				ms = lo + rand.Intn(span+1)
			}
			until := time.Now().Add(time.Duration(ms) * time.Millisecond).UnixNano()
			throttle.noBefore.Store(until)
		}
	}
}

type workItem struct {
	ip       string
	attempts int
}

func worker(ctx context.Context, d *dispatcher, l *List, tester *Tester, cfg ScanCfg, retries, mask int, throttle *globalThrottle) {
	for {
		throttle.wait(ctx)
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
		atomic.AddInt64(&l.Meta.Attempted, 1)

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
