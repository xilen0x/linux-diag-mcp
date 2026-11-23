package sandbox_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec" // tests only: re-exec the test binary so Landlock never restricts the parent
	"strings"
	"syscall"
	"testing"
	"time"

	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"

	"github.com/xilen0x/linux-diag-mcp/internal/sandbox"
)

// childListener selects listenerChild in TestMain.
const childListener = "listener"

// listenerChild mirrors linux-diag-dashboard's startup: bind on loopback,
// then apply the sandbox, then serve. It prints "addr<TAB><addr>" first so
// the parent can connect, then one "name<TAB>result" line per probe.
func listenerChild() int {
	var lc net.ListenConfig
	lc.SetMultipathTCP(false) // as linux-diag-dashboard does
	ln, err := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		return 2
	}
	if _, err := sandbox.Apply(sandbox.Options{Require: true}); err != nil {
		fmt.Fprintln(os.Stderr, "apply:", err)
		return 2
	}
	fmt.Printf("addr\t%s\n", ln.Addr())

	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second))
	acceptErr := func() error {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		_, err = c.Write([]byte("pong"))
		return err
	}()
	fmt.Printf("accept on listener bound before the sandbox\t%s\n", describe(acceptErr))

	// Landlock ABI 4 restricts classic TCP only. Since Go 1.24 net.Listen
	// defaults to Multipath TCP, which Landlock does not restrict, so the
	// classic and multipath cases are probed separately.
	bind := func(mptcp bool) error {
		var lc net.ListenConfig
		lc.SetMultipathTCP(mptcp)
		l, err := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
		if err == nil {
			_ = l.Close()
		}
		return err
	}
	fmt.Printf("bind a new classic TCP listener\t%s\n", describe(bind(false)))
	fmt.Printf("bind a new multipath TCP listener\t%s\n", describe(bind(true)))
	return 0
}

func TestSandboxKeepsListenerBoundBeforeItServing(t *testing.T) {
	abi, err := ll.LandlockGetABIVersion()
	if err != nil || abi < 1 {
		t.Skipf("Landlock not available on this kernel (abi=%d, err=%v)", abi, err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), envChild+"="+childListener)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() {
		t.Fatalf("child printed nothing: %v", sc.Err())
	}
	addr, ok := strings.CutPrefix(sc.Text(), "addr\t")
	if !ok {
		t.Fatalf("first line = %q, want addr", sc.Text())
	}

	// The parent is not sandboxed, so it can connect.
	c, err := net.DialTimeout("tcp4", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial child: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	reply, err := io.ReadAll(c)
	_ = c.Close()
	if err != nil || string(reply) != "pong" {
		t.Fatalf("reply = %q, err = %v; want pong", reply, err)
	}

	got := map[string]string{}
	for sc.Scan() {
		if name, res, ok := strings.Cut(sc.Text(), "\t"); ok {
			got[name] = res
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child failed: %v", err)
	}

	if got["accept on listener bound before the sandbox"] != "ok" {
		t.Errorf("accept: got %q, want ok", got["accept on listener bound before the sandbox"])
	}
	// Not asserted: Landlock has no Multipath TCP restriction at any ABI,
	// so this records what the kernel allows rather than what it should.
	t.Logf("landlock ABI %d; multipath TCP bind inside the sandbox: %s",
		abi, got["bind a new multipath TCP listener"])
	if abi >= 4 { // TCP restrictions need Landlock ABI v4
		if want := syscall.EACCES.Error(); got["bind a new classic TCP listener"] != want {
			t.Errorf("bind a new classic TCP listener: got %q, want %q", got["bind a new classic TCP listener"], want)
		}
	}
}
