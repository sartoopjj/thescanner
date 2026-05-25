package client

import (
	"context"
	"errors"
	"sync"
)

// ErrNoOKIPs is returned by StartDeep when the source list has no OK
// resolvers to score. The web layer maps this to a stable error_code
// the UI can localize.
var ErrNoOKIPs = errors.New("no OK IPs to deep-scan — run a shallow scan first")

// Runner is the per-process scan coordinator. It owns the active scan's
// context (so Pause can cancel it) and delegates persistence to the
// Library. Only one scan or deep-scan can run at a time.
type Runner struct {
	mu     sync.Mutex
	cfg    *Config
	lib    *Library
	log    *LogBus
	active *List // currently running list, if any
	cancel context.CancelFunc
	doneCh chan struct{}
}

func NewRunner(cfg *Config, lib *Library) *Runner {
	return &Runner{cfg: cfg, lib: lib, log: NewLogBus(500)}
}

func (r *Runner) Library() *Library { return r.lib }

// Log returns the shared log bus the active scan publishes to. The web
// layer uses it to back the /api/log/stream SSE endpoint.
func (r *Runner) Log() *LogBus { return r.log }

// ActiveListID returns the ID of the currently running list, or "" if
// nothing is running.
func (r *Runner) ActiveListID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return ""
	}
	return r.active.Meta.ID
}

// StartShallow kicks off the shallow (Level 1) scan on `listID`.
func (r *Runner) StartShallow(listID, serverName string) error {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return errors.New("another scan is already in progress")
	}
	srv := r.findServer(serverName)
	if srv == nil {
		r.mu.Unlock()
		return errors.New("server not found in config")
	}
	l, err := r.lib.Get(listID)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	if l.Meta.Kind == KindManual {
		r.mu.Unlock()
		return errors.New("manual lists can only be deep-scanned")
	}
	l.Meta.Status = ListScanning
	if err := r.lib.Save(l); err != nil {
		r.mu.Unlock()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.doneCh = make(chan struct{})
	r.active = l
	tester := NewTester(*srv, r.cfg.Snapshot().Scan)
	tester.Log = r.log
	tester.ListID = l.Meta.ID
	cfg := r.cfg.Snapshot().Scan
	r.mu.Unlock()

	go func() {
		defer close(r.doneCh)
		save := func() { _ = r.lib.Save(l) }
		Level1(ctx, tester, l, cfg, save)
		l.mu.Lock()
		if ctx.Err() != nil {
			l.Meta.Status = ListPaused
		} else {
			l.Meta.Status = ListDone
		}
		l.mu.Unlock()
		_ = r.lib.Save(l)
		r.mu.Lock()
		r.active = nil
		r.cancel = nil
		r.mu.Unlock()
	}()
	return nil
}

// StartDeep runs Level 2 against `listID`. Works on both shallow and
// manual lists (manual lists have every IP pre-marked OK).
func (r *Runner) StartDeep(listID, serverName string) error {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return errors.New("another scan is already in progress")
	}
	srv := r.findServer(serverName)
	if srv == nil {
		r.mu.Unlock()
		return errors.New("server not found in config")
	}
	l, err := r.lib.Get(listID)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	if l.Meta.OK == 0 && l.Meta.Kind != KindManual {
		r.mu.Unlock()
		return ErrNoOKIPs
	}
	l.Meta.Status = ListDeep
	if err := r.lib.Save(l); err != nil {
		r.mu.Unlock()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.doneCh = make(chan struct{})
	r.active = l
	tester := NewTester(*srv, r.cfg.Snapshot().Scan)
	tester.Log = r.log
	tester.ListID = l.Meta.ID
	l2 := r.cfg.Snapshot().Level2
	r.mu.Unlock()

	go func() {
		defer close(r.doneCh)
		save := func() { _ = r.lib.Save(l) }
		Level2(ctx, tester, l2, l, save)
		l.mu.Lock()
		if ctx.Err() != nil {
			l.Meta.Status = ListPaused
		} else {
			l.Meta.Status = ListDeepDone
		}
		l.mu.Unlock()
		_ = r.lib.Save(l)
		r.mu.Lock()
		r.active = nil
		r.cancel = nil
		r.mu.Unlock()
	}()
	return nil
}

// Pause cancels the in-flight scan (shallow OR deep). The list's state
// is preserved on disk so Resume picks up from where it stopped.
func (r *Runner) Pause() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel == nil {
		return errors.New("nothing running")
	}
	r.cancel()
	return nil
}

// Resume restarts a previously-paused list. The list's current Kind
// + remembered status decide whether it resumes as a shallow or deep
// scan.
func (r *Runner) Resume(listID, serverName string) error {
	l, err := r.lib.Get(listID)
	if err != nil {
		return err
	}
	// Decide which mode based on what the list was doing. If it had
	// any L2 progress we treat the resume as a deep scan; otherwise
	// it's a shallow resume.
	if l.Meta.Status == ListDeep || l.Meta.L2Scored > 0 {
		return r.StartDeep(listID, serverName)
	}
	return r.StartShallow(listID, serverName)
}

// Wait blocks until the active operation finishes; returns immediately
// when nothing is running.
func (r *Runner) Wait() {
	r.mu.Lock()
	d := r.doneCh
	r.mu.Unlock()
	if d != nil {
		<-d
	}
}

func (r *Runner) findServer(name string) *ServerEntry {
	for _, s := range r.cfg.Snapshot().Servers {
		if s.Name == name {
			return &s
		}
	}
	return nil
}
