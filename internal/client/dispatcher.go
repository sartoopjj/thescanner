package client

import "sync"

// dispatcher hands work items to a worker pool. Two queues:
//   - fresh   : never-attempted IPs (preferred)
//   - retry   : IPs awaiting another try (drained only when fresh is empty)
//
// pop() blocks until either queue has work or the pool is finished
// (cancelled, or every worker is idle with no work left to do).
type dispatcher struct {
	mu        sync.Mutex
	cond      *sync.Cond
	fresh     []workItem
	retry     []workItem
	busy      int
	cancelled bool
	// popSeq is an internal monotonic counter incremented inside pop()'s
	// critical section. Tests use it to verify ordering invariants
	// (e.g. "no retry popped while fresh queue was non-empty") without
	// being confused by the non-deterministic order in which workers
	// land their observations into shared slices.
	popSeq int64
}

func newDispatcher() *dispatcher {
	d := &dispatcher{}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func (d *dispatcher) push(it workItem) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if it.attempts == 0 {
		d.fresh = append(d.fresh, it)
	} else {
		d.retry = append(d.retry, it)
	}
	d.cond.Signal()
}

func (d *dispatcher) pop() (workItem, bool) {
	it, _, ok := d.popWithSeq()
	return it, ok
}

// popWithSeq is the same as pop() but also returns a monotonic
// sequence number assigned inside the dispatcher mutex. Tests use it
// to recover true pop order regardless of when each worker lands its
// observation into shared state.
func (d *dispatcher) popWithSeq() (workItem, int64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for {
		if d.cancelled {
			return workItem{}, 0, false
		}
		if len(d.fresh) > 0 {
			it := d.fresh[0]
			d.fresh = d.fresh[1:]
			d.busy++
			d.popSeq++
			return it, d.popSeq, true
		}
		if len(d.retry) > 0 {
			it := d.retry[0]
			d.retry = d.retry[1:]
			d.busy++
			d.popSeq++
			return it, d.popSeq, true
		}
		if d.busy == 0 {
			// No work left and no one is busy — completion.
			d.cond.Broadcast()
			return workItem{}, 0, false
		}
		d.cond.Wait()
	}
}

// done is called after each completed work item.
func (d *dispatcher) done() {
	d.mu.Lock()
	d.busy--
	// Wake any waiters in case completion is now reachable.
	d.cond.Broadcast()
	d.mu.Unlock()
}

// cancel makes future pop() return (false). In-flight work continues until
// the worker checks ctx.Err() and exits.
func (d *dispatcher) cancel() {
	d.mu.Lock()
	d.cancelled = true
	d.cond.Broadcast()
	d.mu.Unlock()
}
