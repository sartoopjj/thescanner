package client

import (
	"math/rand"
	"testing"
)

func TestShouldNoise_Disabled(t *testing.T) {
	rand.Seed(1)
	if shouldNoise(0) {
		t.Fatal("every=0 must be off")
	}
	if shouldNoise(-1) {
		t.Fatal("every<0 must be off")
	}
}

// Rate sanity: across many trials, fires roughly at 1/(2*every). We
// allow a generous ±50% band — the test is here to catch "always fires"
// or "never fires" regressions, not to pin an exact rate.
func TestShouldNoise_RateRoughlyHalfOfEvery(t *testing.T) {
	rand.Seed(42)
	const trials = 100_000
	for _, every := range []int{5, 30, 100} {
		fires := 0
		for i := 0; i < trials; i++ {
			if shouldNoise(every) {
				fires++
			}
		}
		want := float64(trials) / float64(2*every)
		got := float64(fires)
		lo, hi := want*0.5, want*1.5
		if got < lo || got > hi {
			t.Fatalf("every=%d: fires=%d, want roughly %.0f (band %.0f..%.0f)",
				every, fires, want, lo, hi)
		}
	}
}
