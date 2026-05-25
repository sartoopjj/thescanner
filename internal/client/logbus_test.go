package client

import (
	"sync"
	"testing"
	"time"
)

func TestLogBus_PublishFansOutToSubscribers(t *testing.T) {
	bus := NewLogBus(10)
	chA, cancelA := bus.Subscribe(8)
	defer cancelA()
	chB, cancelB := bus.Subscribe(8)
	defer cancelB()

	var wg sync.WaitGroup
	wg.Add(2)
	gotA, gotB := 0, 0
	go func() {
		defer wg.Done()
		for range chA {
			gotA++
			if gotA == 3 {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range chB {
			gotB++
			if gotB == 3 {
				return
			}
		}
	}()

	for i := 0; i < 3; i++ {
		bus.Publish(LogEvent{Kind: "query", IP: "1.1.1.1"})
	}
	wg.Wait()
	if gotA != 3 || gotB != 3 {
		t.Fatalf("counts: A=%d B=%d", gotA, gotB)
	}
}

func TestLogBus_RingBufferKeepsRecent(t *testing.T) {
	bus := NewLogBus(3)
	for i := 0; i < 5; i++ {
		bus.Publish(LogEvent{Kind: "info", Message: itoa(i)})
	}
	r := bus.Recent()
	if len(r) != 3 {
		t.Fatalf("ring size: %d", len(r))
	}
	if r[0].Message != "2" || r[2].Message != "4" {
		t.Fatalf("ring contents: %+v", r)
	}
}

func TestLogBus_SlowSubscriberDropsRatherThanBlocks(t *testing.T) {
	bus := NewLogBus(10)
	_, cancel := bus.Subscribe(1) // buffer of 1; we never read
	defer cancel()

	// 100 publishes complete promptly even though the subscriber can't
	// keep up — Publish must drop, not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			bus.Publish(LogEvent{Kind: "query"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on slow subscriber")
	}
}

// helper — keep this file self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 4)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
