package sandbox_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec" // tests only: re-exec the test binary so Landlock never restricts the parent
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/systemd"
	"github.com/xilen0x/linux-diag-mcp/internal/sandbox"
)

const (
	envChild   = "LINUX_DIAG_MCP_SANDBOX_CHILD"
	envTempDir = "LINUX_DIAG_MCP_SANDBOX_TMPDIR"
	envTCPAddr = "LINUX_DIAG_MCP_SANDBOX_TCPADDR"
)

// TestMain turns the test binary into the sandboxed child when re-executed.
// Landlock cannot be undone, so it must never be applied to the parent.
func TestMain(m *testing.M) {
	switch os.Getenv(envChild) {
	case "1":
		os.Exit(sandboxedChild())
	case childListener:
		os.Exit(listenerChild())
	}
	os.Exit(m.Run())
}

// probe is one access attempted inside the sandbox.
type probe struct {
	name string
	op   func() error
}

// sandboxedChild applies the real sandbox, then reports each probe as
// "name<TAB>ok" or "name<TAB>errno" on stdout.
func sandboxedChild() int {
	if _, err := sandbox.Apply(sandbox.Options{Require: true}); err != nil {
		fmt.Fprintln(os.Stderr, "apply:", err)
		return 2
	}
	readFile := func(p string) func() error {
		return func() error { _, err := os.ReadFile(p); return err }
	}
	readDir := func(p string) func() error {
		return func() error { _, err := os.ReadDir(p); return err }
	}
	probes := []probe{
		{"read /proc/self/status", readFile("/proc/self/status")},
		{"readdir /proc", readDir("/proc")},
		{"readdir /sys", readDir("/sys")},
		{"read /etc/os-release", readFile("/etc/os-release")},
		{"read /etc/passwd", readFile("/etc/passwd")},
		{"read /proc/self/root/etc/passwd", readFile("/proc/self/root/etc/passwd")},
		{"readdir /home", readDir("/home")},
		{"readdir /", readDir("/")},
		{"create file in tempdir", func() error {
			return os.WriteFile(filepath.Join(os.Getenv(envTempDir), "x"), []byte("x"), 0o600)
		}},
		{"write /proc/self/comm", func() error {
			return os.WriteFile("/proc/self/comm", []byte("pwned"), 0)
		}},
		{"dbus failed units", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := systemd.FailedUnits(ctx, systemd.DialSystemBus)
			return err
		}},
		{"tcp connect", func() error {
			c, err := net.Dial("tcp4", os.Getenv(envTCPAddr))
			if err == nil {
				_ = c.Close()
			}
			return err
		}},
	}
	for _, p := range probes {
		fmt.Printf("%s\t%s\n", p.name, describe(p.op()))
	}
	return 0
}

func describe(err error) string {
	var errno syscall.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &errno):
		return errno.Error()
	default:
		return "other: " + err.Error()
	}
}

func TestSandboxRestrictsFilesystemAndNetwork(t *testing.T) {
	abi, err := ll.LandlockGetABIVersion()
	if err != nil || abi < 1 {
		t.Skipf("Landlock not available on this kernel (abi=%d, err=%v)", abi, err)
	}
	for _, p := range []string{"/etc/passwd", "/home", "/etc/os-release"} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("fixture path %s missing: %v", p, err)
		}
	}

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	tmp := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		envChild+"=1", envTempDir+"="+tmp, envTCPAddr+"="+ln.Addr().String())
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sandboxed child failed: %v", err)
	}

	got := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if name, res, ok := strings.Cut(sc.Text(), "\t"); ok {
			got[name] = res
		}
	}

	denied := syscall.EACCES.Error()
	want := map[string]string{
		"read /proc/self/status":          "ok",
		"readdir /proc":                   "ok",
		"readdir /sys":                    "ok",
		"read /etc/os-release":            "ok", // symlink to /usr/lib/os-release
		"read /etc/passwd":                denied,
		"read /proc/self/root/etc/passwd": denied, // magic-link escape
		"readdir /home":                   denied,
		"readdir /":                       denied,
		"create file in tempdir":          denied,
	}
	// The D-Bus socket is the one connect(2) target the sandbox allows
	// (explicitly needed from Landlock ABI v9 on).
	_, busErr := os.Stat(systemd.SystemBusSocket)
	if busErr == nil {
		want["dbus failed units"] = "ok"
	}
	// Record what this kernel actually exercised: the ResolveUnix rule is
	// only enforced (and so only proven) from ABI 9 with the bus present.
	t.Logf("landlock ABI %d; dbus probe checked: %t; unix-socket rule enforced: %t",
		abi, busErr == nil, abi >= 9 && busErr == nil)
	if abi >= 4 { // TCP restrictions need Landlock ABI v4
		want["tcp connect"] = denied
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%-34s got %q, want %q", name, got[name], w)
		}
	}
	// /proc/self/comm is writable by the owner without Landlock; inside the
	// sandbox it must fail (EACCES), whatever the exact errno.
	if got["write /proc/self/comm"] == "ok" {
		t.Errorf("write /proc/self/comm succeeded inside the sandbox")
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Errorf("child created %d file(s) in %s", len(entries), tmp)
	}
}
