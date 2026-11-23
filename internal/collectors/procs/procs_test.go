package procs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procs"
	"github.com/xilen0x/linux-diag-mcp/internal/validate"
)

// phasedFS serves testdata/sample0 until "uptime" has been opened twice,
// then testdata/sample1. Collect reads uptime first in each sample, so each
// sample sees one consistent tree. It records every opened path.
type phasedFS struct {
	mu      sync.Mutex
	trees   [2]fs.FS
	uptimes int
	opened  []string
}

func newPhasedFS() *phasedFS {
	return &phasedFS{trees: [2]fs.FS{os.DirFS("testdata/sample0"), os.DirFS("testdata/sample1")}}
}

func (p *phasedFS) Open(name string) (fs.File, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opened = append(p.opened, name)
	if name == "uptime" {
		p.uptimes++
	}
	phase := 0
	if p.uptimes >= 2 {
		phase = 1
	}
	return p.trees[phase].Open(name)
}

func collect(t *testing.T, fsys fs.FS, sortBy validate.SortKey, limit int) procs.Result {
	t.Helper()
	got, err := procs.Collect(context.Background(), fsys, procs.Options{
		SortBy: sortBy, Limit: limit, PageSize: 4096, ClockTicks: 100,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return got
}

func checkProcesses(t *testing.T, got, want []procs.Process) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d processes:\n %+v\nwant %d:\n %+v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %+v\n     want %+v", i, got[i], want[i])
		}
	}
}

// Expected values computed by hand: the window is 1000.50-1000.00 = 0.5 s,
// so with 100 ticks/s, 50 ticks = 100% of one CPU.
var (
	bash    = procs.Process{PID: 101502, Comm: "bash", State: "R", CPUPercent: 100, RSSBytes: 874 * 4096}
	reused  = procs.Process{PID: 6666, Comm: "reused", State: "S", CPUPercent: 40, RSSBytes: 50 * 4096}
	systemd = procs.Process{PID: 1, Comm: "systemd", State: "S", CPUPercent: 30, RSSBytes: 3595 * 4096}
	newborn = procs.Process{PID: 7777, Comm: "newborn", State: "Z", CPUPercent: 20, RSSBytes: 0}
	hostile = procs.Process{PID: 4242, Comm: "a) (b[2J", State: "R", CPUPercent: 10, RSSBytes: 100 * 4096}
	kthread = procs.Process{PID: 2, Comm: "kthreadd", State: "S", CPUPercent: 0, RSSBytes: 0}
)

func TestSortByCPU(t *testing.T) {
	got := collect(t, newPhasedFS(), validate.SortByCPU, 20)
	if got.SortBy != "cpu" || got.Limit != 20 || got.TotalProcesses != 6 {
		t.Errorf("header = %+v", got)
	}
	checkProcesses(t, got.Processes, []procs.Process{bash, reused, systemd, newborn, hostile, kthread})
}

func TestSortByMemWithLimit(t *testing.T) {
	got := collect(t, newPhasedFS(), validate.SortByMem, 5)
	// Equal RSS (newborn, kthread: 0) is broken by CPU, then PID.
	checkProcesses(t, got.Processes, []procs.Process{systemd, bash, hostile, reused, newborn})
	if got.TotalProcesses != 6 {
		t.Errorf("total = %d, want 6", got.TotalProcesses)
	}
}

func TestLimitTruncates(t *testing.T) {
	got := collect(t, newPhasedFS(), validate.SortByCPU, 1)
	checkProcesses(t, got.Processes, []procs.Process{bash})
}

// Security requirement 4: cmdline and environ are never read, even though
// the fixture provides them with secrets inside.
func TestNeverReadsCmdlineOrEnviron(t *testing.T) {
	fsys := newPhasedFS()
	got := collect(t, fsys, validate.SortByCPU, 20)
	for _, name := range fsys.opened {
		switch base := path.Base(name); base {
		case "stat", "statm", "uptime", ".":
		default:
			t.Errorf("opened unexpected file %q", name)
		}
	}
	out, _ := json.Marshal(got)
	if strings.Contains(string(out), "hunter2") {
		t.Fatalf("secret leaked into output: %s", out)
	}
}

func TestRejectsInvalidOptions(t *testing.T) {
	for name, o := range map[string]procs.Options{
		"zero sort key": {Limit: 10, PageSize: 4096, ClockTicks: 100},
		"limit 0":       {SortBy: validate.SortByCPU, Limit: 0, PageSize: 4096, ClockTicks: 100},
		"limit 21":      {SortBy: validate.SortByCPU, Limit: 21, PageSize: 4096, ClockTicks: 100},
		"no page size":  {SortBy: validate.SortByCPU, Limit: 10, ClockTicks: 100},
		"no clock":      {SortBy: validate.SortByCPU, Limit: 10, PageSize: 4096},
	} {
		if _, err := procs.Collect(context.Background(), newPhasedFS(), o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := procs.Collect(ctx, newPhasedFS(), procs.Options{
		SortBy: validate.SortByCPU, Limit: 10, Interval: time.Hour, PageSize: 4096, ClockTicks: 100,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("did not return promptly")
	}
}
