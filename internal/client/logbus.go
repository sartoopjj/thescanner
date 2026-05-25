package client

import (
	"sync"
	"time"
)

// LogEvent is a single line of "what's happening right now" for the
// live log shown at the bottom of the list view. Cheap to construct;
// publishers shouldn't think about backpressure (slow subscribers get
// dropped silently — see Subscribe).
type LogEvent struct {
	Time    time.Time `json:"time"`
	ListID  string    `json:"list_id,omitempty"`
	Kind    string    `json:"kind"` // "query" | "noise" | "info" | "warn"
	IP      string    `json:"ip,omitempty"`
	Domain  string    `json:"domain,omitempty"`
	QName   string    `json:"qname,omitempty"`   // full DNS name actually sent
	Status  string    `json:"status,omitempty"`  // "ok" | "fail"
	Reason  string    `json:"reason,omitempty"`
	RTTMs   int64     `json:"rtt_ms,omitempty"`
	QLen    int       `json:"q_len,omitempty"`   // query plaintext bytes
	RespLen int       `json:"resp_len,omitempty"` // response ciphertext bytes
	Message string    `json:"message,omitempty"`
}

// LogBus is a fan-out broadcaster with a small ring buffer so a new
// subscriber gets immediate context instead of an empty pane.
type LogBus struct {
	mu          sync.Mutex
	subscribers map[chan LogEvent]struct{}
	ring        []LogEvent
	ringCap     int
}

// NewLogBus reserves cap slots for the recent-events ring buffer.
func NewLogBus(cap int) *LogBus {
	if cap <= 0 {
		cap = 200
	}
	return &LogBus{
		subscribers: map[chan LogEvent]struct{}{},
		ring:        make([]LogEvent, 0, cap),
		ringCap:     cap,
	}
}

// Publish broadcasts an event. Non-blocking — subscribers whose channels
// are full get the event dropped (we keep the log "best-effort" so a
// stalled browser tab can't back the publisher up).
func (b *LogBus) Publish(e LogEvent) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	b.mu.Lock()
	if len(b.ring) >= b.ringCap {
		b.ring = append(b.ring[:0], b.ring[1:]...)
	}
	b.ring = append(b.ring, e)
	subs := make([]chan LogEvent, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// subscriber is too slow; drop this event for them
		}
	}
}

// Subscribe registers a new listener. Returns a channel of events plus
// an unsubscribe func. The channel is buffered so brief bursts don't
// drop events on a healthy subscriber.
func (b *LogBus) Subscribe(buffer int) (<-chan LogEvent, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan LogEvent, buffer)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subscribers[ch]; ok {
			delete(b.subscribers, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Recent returns a snapshot of the ring buffer (oldest first). Used by
// new HTTP subscribers to backfill before they start tailing.
func (b *LogBus) Recent() []LogEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]LogEvent, len(b.ring))
	copy(out, b.ring)
	return out
}
