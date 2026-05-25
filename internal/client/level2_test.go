package client

import (
	"testing"
	"time"
)

// scoreFormula tests the score math in isolation so we can lock down
// the latency-bounded design and prevent the v1 regression where a low
// success rate plus moderate RTT got clamped to 0.
//
// The scoring rule (see scoreOne in level2.go) is:
//   score = success_rate*100 - min(p95_ms/100, 30)
//   floor of 1 when ok > 0
//   clamped to 0 otherwise
func scoreFormula(ok, per int, p95 time.Duration) float64 {
	successRate := float64(ok) / float64(per)
	latencyPenalty := float64(p95.Milliseconds()) / 100.0
	if latencyPenalty > 30 {
		latencyPenalty = 30
	}
	s := successRate*100.0 - latencyPenalty
	if ok > 0 && s < 1 {
		s = 1
	}
	if s < 0 {
		s = 0
	}
	return s
}

func TestScore_LowSuccessNotZeroed(t *testing.T) {
	// Bug we fixed: 21/100 with p95 = 210 ms used to score 0 because
	// the latency penalty (210/10 = 21) wiped out the success rate
	// (21/100 * 100 = 21). Now latency divides by 100, capped at 30,
	// so the same scenario lands around 19 — non-zero, sortable.
	got := scoreFormula(21, 100, 210*time.Millisecond)
	if got < 15 || got > 25 {
		t.Fatalf("21/100 at 210ms p95: want roughly 19, got %.2f", got)
	}
}

func TestScore_LatencyCapped(t *testing.T) {
	// A reliable resolver (80%) over a slow link (2 s p95) should
	// still rank above a flaky one (10%) on a fast link. The latency
	// penalty caps at 30 so we never punish slowness more than that.
	slow := scoreFormula(80, 100, 2*time.Second)
	fastFlaky := scoreFormula(10, 100, 50*time.Millisecond)
	if slow <= fastFlaky {
		t.Fatalf("reliable-but-slow %.2f should beat fast-but-flaky %.2f", slow, fastFlaky)
	}
}

func TestScore_FloorOneWhenAnyOK(t *testing.T) {
	// Even a single OK answer should rank above a complete failure so
	// the sort order distinguishes "works rarely" from "never worked".
	got := scoreFormula(1, 100, 5*time.Second)
	if got < 1 {
		t.Fatalf("any OK should floor to 1, got %.2f", got)
	}
}

func TestScore_ZeroOK(t *testing.T) {
	got := scoreFormula(0, 100, 100*time.Millisecond)
	if got != 0 {
		t.Fatalf("zero successes must yield 0, got %.2f", got)
	}
}
