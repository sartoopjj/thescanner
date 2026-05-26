package client

import (
	"strings"
	"testing"
)

func TestExpandLine_BareIP(t *testing.T) {
	got := expandLine("1.2.3.4")
	if len(got) != 1 || got[0] != "1.2.3.4" {
		t.Fatalf("got %v", got)
	}
}

func TestExpandLine_CIDR24(t *testing.T) {
	got := expandLine("10.0.0.0/24")
	if len(got) != 256 {
		t.Fatalf("/24 = 256 hosts, got %d", len(got))
	}
	if got[0] != "10.0.0.0" || got[255] != "10.0.0.255" {
		t.Fatalf("bad endpoints: %s..%s", got[0], got[255])
	}
}

func TestExpandLine_CIDR28(t *testing.T) {
	got := expandLine("192.168.1.16/28")
	if len(got) != 16 {
		t.Fatalf("/28 = 16 hosts, got %d", len(got))
	}
	if got[0] != "192.168.1.16" || got[15] != "192.168.1.31" {
		t.Fatalf("bad endpoints: %s..%s", got[0], got[15])
	}
}

func TestExpandLine_RejectsTooBroad(t *testing.T) {
	if got := expandLine("0.0.0.0/0"); got != nil {
		t.Fatalf("/0 must be refused, got %d entries", len(got))
	}
	if got := expandLine("10.0.0.0/7"); got != nil {
		t.Fatalf("/7 must be refused, got %d entries", len(got))
	}
}

func TestExpandLine_StripsComments(t *testing.T) {
	got := expandLine("1.2.3.4  # comment here")
	if len(got) != 1 || got[0] != "1.2.3.4" {
		t.Fatalf("got %v", got)
	}
	if got := expandLine("# whole line comment"); got != nil {
		t.Fatalf("comment-only line should yield nothing, got %v", got)
	}
}

func TestExpandLine_RejectsGarbage(t *testing.T) {
	cases := []string{"", "not an ip", "999.999.999.999", "1.2.3"}
	for _, c := range cases {
		if got := expandLine(c); got != nil {
			t.Errorf("%q should yield nil, got %v", c, got)
		}
	}
}

func TestAppendIPsFromReader_Shallow(t *testing.T) {
	lib, err := NewLibrary(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := lib.CreateShallowEmpty("test", "srv")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader(`
1.1.1.1
8.8.8.8
# a comment
10.0.0.0/30
1.1.1.1
`)
	added, err := lib.AppendIPsFromReader(l, input)
	if err != nil {
		t.Fatal(err)
	}
	// 2 bare + 4 from /30 = 6, dupe of 1.1.1.1 dropped
	if added != 6 {
		t.Fatalf("added = %d, want 6", added)
	}
	if l.Meta.Total != 6 {
		t.Fatalf("Total = %d, want 6", l.Meta.Total)
	}
	if _, ok := l.IPs["1.1.1.1"]; !ok {
		t.Fatal("1.1.1.1 missing")
	}
	if _, ok := l.IPs["10.0.0.3"]; !ok {
		t.Fatal("10.0.0.3 missing (last addr of /30)")
	}
	// Shallow MUST NOT pre-allocate Result entries — that's what kept
	// peak RAM bounded on million-IP lists.
	if len(l.Results) != 0 {
		t.Fatalf("shallow Results map should be empty pre-scan, got %d", len(l.Results))
	}
}

func TestAppendIPsFromReader_Manual(t *testing.T) {
	lib, _ := NewLibrary(t.TempDir())
	l, _ := lib.CreateManualEmpty("manual")
	added, err := lib.AppendIPsFromReader(l, strings.NewReader("1.2.3.4\n5.6.7.8\n"))
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 || l.Meta.Total != 2 || l.Meta.OK != 2 {
		t.Fatalf("added=%d total=%d ok=%d", added, l.Meta.Total, l.Meta.OK)
	}
	// Manual lists DO pre-populate Results (deep-scan needs the OK marker).
	if len(l.Results) != 2 {
		t.Fatalf("manual Results map should be populated, got %d", len(l.Results))
	}
}

func BenchmarkAppendIPsFromReader_100k(b *testing.B) {
	var buf strings.Builder
	for i := 0; i < 100000; i++ {
		buf.WriteString("10.0.")
		buf.WriteString(itoa(i / 256))
		buf.WriteByte('.')
		buf.WriteString(itoa(i % 256))
		buf.WriteByte('\n')
	}
	input := buf.String()
	b.ResetTimer()
	b.SetBytes(int64(len(input)))
	for i := 0; i < b.N; i++ {
		lib, _ := NewLibrary(b.TempDir())
		l, _ := lib.CreateShallowEmpty("bench", "srv")
		if _, err := lib.AppendIPsFromReader(l, strings.NewReader(input)); err != nil {
			b.Fatal(err)
		}
	}
}

