package validate_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xilen0x/linux-diag-mcp/internal/validate"
)

// assertRejected checks that err is a *validate.Error whose message is
// exactly want. A constant message proves the rejected input is never
// reflected back: it may be hostile (prompt injection) or huge.
func assertRejected(t *testing.T, input string, err error, want string) {
	t.Helper()
	var verr *validate.Error
	if !errors.As(err, &verr) {
		t.Fatalf("input %q: err = %v, want *validate.Error", input, err)
	}
	if err.Error() != want {
		t.Fatalf("input %q: message %q differs from the constant %q", input, err.Error(), want)
	}
}

// Expected constant messages, one per parameter.
const (
	sortByMsg   = `invalid sort_by: must be one of "cpu", "mem"`
	protocolMsg = `invalid protocol: must be one of "tcp", "udp"`
	limitMsg    = `invalid limit: must be an integer in [1, 20]`
	unitMsg     = `invalid unit: must match ^[a-zA-Z0-9@._-]{1,128}\.service$`
)

func TestParseSortKey(t *testing.T) {
	for in, want := range map[string]validate.SortKey{
		"cpu": validate.SortByCPU,
		"mem": validate.SortByMem,
	} {
		got, err := validate.ParseSortKey(in)
		if err != nil || got != want {
			t.Errorf("ParseSortKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "CPU", "Mem", " cpu", "cpu ", "cpu\n", "cpu\x00", "memory", "rss",
		"cpu,mem", "cpu;mem", "ｃｐｕ", "сpu" /* Cyrillic с */, "\xff",
	} {
		_, err := validate.ParseSortKey(in)
		assertRejected(t, in, err, sortByMsg)
	}
}

func TestParseProtocol(t *testing.T) {
	for in, want := range map[string]validate.Protocol{
		"tcp": validate.ProtocolTCP,
		"udp": validate.ProtocolUDP,
	} {
		got, err := validate.ParseProtocol(in)
		if err != nil || got != want {
			t.Errorf("ParseProtocol(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "TCP", "tcp6", "udp6", "tcp ", "raw", "unix", "icmp", "tcp|udp",
		"../tcp", "/proc/net/tcp", "tcp\x00", "ｔｃｐ",
	} {
		_, err := validate.ParseProtocol(in)
		assertRejected(t, in, err, protocolMsg)
	}
}

func TestLimit(t *testing.T) {
	ptr := func(v int) *int { return &v }

	if got, err := validate.Limit(nil); err != nil || got != validate.DefaultLimit {
		t.Fatalf("Limit(nil) = %d, %v; want default %d", got, err, validate.DefaultLimit)
	}
	for _, v := range []int{1, 10, 20} {
		if got, err := validate.Limit(ptr(v)); err != nil || got != v {
			t.Errorf("Limit(%d) = %d, %v", v, got, err)
		}
	}
	for _, v := range []int{0, -1, 21, 1 << 31, -(1 << 31), int(^uint(0) >> 1)} {
		_, err := validate.Limit(ptr(v))
		assertRejected(t, strconv.Itoa(v), err, limitMsg)
	}
}

func TestParseUnitNameAccepts(t *testing.T) {
	for _, in := range []string{
		"nginx.service",
		"a.service",
		"getty@tty1.service",
		"user@1000.service",
		"systemd-journald.service",
		"dbus-org.freedesktop.login1.service",
		"Under_Score.service",
		strings.Repeat("a", 128) + ".service", // max stem length
	} {
		u, err := validate.ParseUnitName(in)
		if err != nil {
			t.Errorf("ParseUnitName(%q): %v", in, err)
			continue
		}
		if u.String() != in {
			t.Errorf("String() = %q, want %q", u.String(), in)
		}
	}
}

func TestParseUnitNameRejectsMalicious(t *testing.T) {
	cases := map[string][]string{
		"empty and suffix only": {"", ".service", "service", "nginx"},
		"wrong type or case": {
			"nginx.socket", "nginx.timer", "nginx.SERVICE", "nginx.Service",
			"nginx.service.d", "nginx.servicex", "nginx.service.", "nginx.servic",
		},
		"path traversal": {
			"../../etc/passwd", "../nginx.service", "/etc/passwd.service",
			"foo/../bar.service", "..%2f..%2fetc%2fpasswd.service",
			"..\\..\\nginx.service", "/run/systemd/system/x.service",
			"%2e%2e%2fnginx.service",
		},
		"shell and command injection": {
			"nginx.service; rm -rf /", "nginx.service && id", "nginx.service|id",
			"$(id).service", "`id`.service", "nginx.service #", "nginx service",
			"nginx.service\nfoo.service", "nginx.service\n", "\nnginx.service",
			"nginx.service\r", "nginx\x00.service", "nginx.service\x00",
			"nginx\t.service", "'nginx'.service", "\"nginx\".service",
			"nginx*.service", "nginx?.service", "{a,b}.service", "~root.service",
		},
		"weird unicode": {
			"ｎｇｉｎｘ.service",       // fullwidth
			"nginх.service",       // Cyrillic х
			"ngınx.service",       // dotless ı
			"nginx.ѕervice",       // Cyrillic ѕ in suffix
			"\u202Enginx.service", // right-to-left override
			"ng\u200Binx.service", // zero-width space
			"nginx\u0301.service", // combining accent
			"nginx\uFEFF.service", // BOM
			"\xff\xfe.service",    // invalid UTF-8
			"nginx\u2024service",  // one-dot leader instead of '.'
			"nginx.service\u0000", // NUL
			"🔥.service",           // emoji
		},
		"extreme lengths": {
			strings.Repeat("a", 129) + ".service",
			strings.Repeat("a", 1<<20) + ".service",
			strings.Repeat(".", 1<<16) + "service",
		},
	}
	for group, inputs := range cases {
		t.Run(group, func(t *testing.T) {
			for _, in := range inputs {
				_, err := validate.ParseUnitName(in)
				assertRejected(t, in, err, unitMsg)
			}
		})
	}
}

func TestZeroUnitNameIsInvalid(t *testing.T) {
	var u validate.UnitName
	if u.Valid() {
		t.Fatal("zero UnitName reports Valid()")
	}
	u, _ = validate.ParseUnitName("nginx.service")
	if !u.Valid() {
		t.Fatal("parsed UnitName reports !Valid()")
	}
}

// FuzzParseUnitName checks the invariants of accepted names independently
// of the regex: ASCII-only allowed charset, bounded length, .service suffix.
func FuzzParseUnitName(f *testing.F) {
	for _, s := range []string{
		"nginx.service", "getty@tty1.service", "../x.service", "a\x00.service",
		"ｎ.service", strings.Repeat("a", 128) + ".service", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		u, err := validate.ParseUnitName(in)
		if err != nil {
			assertRejected(t, in, err, unitMsg)
			return
		}
		s := u.String()
		if s != in {
			t.Fatalf("accepted %q but String() = %q", in, s)
		}
		if !utf8.ValidString(s) || len(s) > 128+len(".service") || !strings.HasSuffix(s, ".service") {
			t.Fatalf("accepted name violates invariants: %q", s)
		}
		stem := strings.TrimSuffix(s, ".service")
		if stem == "" {
			t.Fatalf("accepted empty stem: %q", s)
		}
		for _, r := range stem {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
				r == '@' || r == '.' || r == '_' || r == '-'
			if !ok {
				t.Fatalf("accepted forbidden rune %q in %q", r, s)
			}
		}
	})
}
