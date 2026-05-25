package client

import (
	"testing"
)

// recoverInterruptedScans is called by NewRunner. Lists that were
// running when the previous process exited (status=scanning or =deep)
// must be flipped to paused so the UI doesn't show a fake "scanning"
// badge with no worker behind it — that was the bug where users had to
// press Resume on app restart.
func TestRunner_RecoverInterruptedScans(t *testing.T) {
	dir := t.TempDir()
	lib, err := NewLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	scanning, _ := lib.CreateShallow("a", "srv", []string{"1.1.1.1"})
	deep, _ := lib.CreateShallow("b", "srv", []string{"2.2.2.2"})
	paused, _ := lib.CreateShallow("c", "srv", []string{"3.3.3.3"})
	done, _ := lib.CreateShallow("d", "srv", []string{"4.4.4.4"})

	// Simulate the previous process crashing mid-scan.
	scanning.Meta.Status = ListScanning
	deep.Meta.Status = ListDeep
	paused.Meta.Status = ListPaused
	done.Meta.Status = ListDone
	if err := lib.Save(scanning); err != nil {
		t.Fatal(err)
	}
	if err := lib.Save(deep); err != nil {
		t.Fatal(err)
	}
	if err := lib.Save(paused); err != nil {
		t.Fatal(err)
	}
	if err := lib.Save(done); err != nil {
		t.Fatal(err)
	}

	// Build a fresh Library (simulates process restart reading lists
	// from disk) and feed it to a new Runner, which should recover.
	lib2, err := NewLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.data = DefaultData()
	_ = NewRunner(cfg, lib2)

	// scanning + deep should now be paused; paused + done untouched.
	cases := []struct {
		id   string
		want ListStatus
	}{
		{scanning.Meta.ID, ListPaused},
		{deep.Meta.ID, ListPaused},
		{paused.Meta.ID, ListPaused},
		{done.Meta.ID, ListDone},
	}
	for _, c := range cases {
		got, err := lib2.Get(c.id)
		if err != nil {
			t.Fatalf("get %s: %v", c.id, err)
		}
		if got.Meta.Status != c.want {
			t.Errorf("list %s: status = %s, want %s", c.id, got.Meta.Status, c.want)
		}
	}
}
