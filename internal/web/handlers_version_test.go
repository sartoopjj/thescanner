package web

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.4", "v1.2.3", true},
		{"v1.3.0", "v1.2.99", true},
		{"v2.0.0", "v1.99.99", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.4", false}, // older latest → false
		{"1.2.4", "1.2.3", true},    // missing leading v
		{"v1.2.4", "dev", false},    // dev → up-to-date
		{"v1.2.4", "unknown", false},
		{"v1.2.4", "", false},
		{"v1.2.4-rc1", "v1.2.3", true},  // pre-release suffix ignored
		{"v1.2.4", "v1.2.4-rc1", false}, // current is rc; numeric equal → not newer
	}
	for _, tc := range cases {
		if got := isNewer(tc.latest, tc.current); got != tc.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}
