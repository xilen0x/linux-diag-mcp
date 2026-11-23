package procio_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
)

func TestReadFile(t *testing.T) {
	fsys := fstest.MapFS{
		"small": {Data: []byte("hello")},
		"big":   {Data: []byte(strings.Repeat("x", 101))},
	}
	got, err := procio.ReadFile(fsys, "small", 100)
	if err != nil || string(got) != "hello" {
		t.Fatalf("ReadFile(small) = %q, %v", got, err)
	}
	if _, err := procio.ReadFile(fsys, "big", 100); err == nil {
		t.Fatal("ReadFile(big) succeeded, want size-limit error")
	}
	if _, err := procio.ReadFile(fsys, "missing", 100); err == nil {
		t.Fatal("ReadFile(missing) succeeded")
	}
}

func TestCleanText(t *testing.T) {
	cases := map[string]string{
		"Ubuntu 22.04.5 LTS":                    "Ubuntu 22.04.5 LTS",
		"a\nb\tc\x00d\x1be[31m":                 "abcde[31m",
		"bad\xffutf8":                           "badutf8",
		"rtl\u202Eoverride":                     "rtloverride",
		"Debian GNU/Linux 12 (bookworm)":        "Debian GNU/Linux 12 (bookworm)",
		"Fedora Linux 40 (Workstation Edition)": "Fedora Linux 40 (Workstation Edition)",
		strings.Repeat("é", 200):                strings.Repeat("é", procio.MaxTextRunes),
	}
	for in, want := range cases {
		if got := procio.CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPercent(t *testing.T) {
	cases := []struct {
		part, whole uint64
		want        float64
	}{
		{32, 82, 39.02},
		{1, 3, 33.33},
		{0, 0, 0}, // never NaN: NaN breaks JSON encoding
		{5, 0, 0},
		{10, 10, 100},
	}
	for _, c := range cases {
		if got := procio.Percent(c.part, c.whole); got != c.want {
			t.Errorf("Percent(%d, %d) = %v, want %v", c.part, c.whole, got, c.want)
		}
	}
}
