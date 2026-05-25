package server

import (
	"testing"
)

func TestStats_RecordIdx_Survives_Reload(t *testing.T) {
	s := NewStats("")
	s.SetTokenNames([]string{"alice", "bob"})

	for i := 0; i < 5; i++ {
		s.RecordIdx(0)
	}
	for i := 0; i < 3; i++ {
		s.RecordIdx(1)
	}
	snap := s.Snapshot()
	if snap.PerToken["alice"] != 5 || snap.PerToken["bob"] != 3 {
		t.Fatalf("pre-reload: %+v", snap.PerToken)
	}

	// Reload with bob removed, "carol" added. alice + bob counts preserved.
	s.SetTokenNames([]string{"alice", "carol"})
	s.RecordIdx(1) // carol +1
	snap = s.Snapshot()
	if snap.PerToken["alice"] != 5 {
		t.Fatalf("alice clobbered: %+v", snap.PerToken)
	}
	if snap.PerToken["bob"] != 3 {
		t.Fatalf("bob disappeared on reload: %+v", snap.PerToken)
	}
	if snap.PerToken["carol"] != 1 {
		t.Fatalf("carol missing: %+v", snap.PerToken)
	}
}

func BenchmarkStats_RecordIdx_Contended(b *testing.B) {
	s := NewStats("")
	s.SetTokenNames([]string{"alice", "bob", "carol"})
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.RecordIdx(0)
		}
	})
}

