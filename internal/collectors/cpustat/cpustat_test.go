package cpustat_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/cpustat"
)

// sequenceFS serves a different /proc/stat on every Open, simulating the
// two samples taken over time.
type sequenceFS struct {
	mu      sync.Mutex
	samples [][]byte
	opens   int
}

func (s *sequenceFS) Open(name string) (fs.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "stat" || s.opens >= len(s.samples) {
		return nil, fs.ErrNotExist
	}
	data := s.samples[s.opens]
	s.opens++
	return fstest.MapFS{"stat": {Data: data}}.Open("stat")
}

func fixtureFS(t *testing.T) *sequenceFS {
	t.Helper()
	var samples [][]byte
	for _, name := range []string{"testdata/stat.0", "testdata/stat.1"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, b)
	}
	return &sequenceFS{samples: samples}
}

func TestCollectFixture(t *testing.T) {
	got, err := cpustat.Collect(context.Background(), fixtureFS(t), 0)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// Expected values computed by hand from the fixture deltas:
	// aggregate Δ user+nice=22 system=10 idle=22 iowait=28 → total 82, busy 32.
	want := cpustat.Usage{
		UsagePercent:  39.02,
		UserPercent:   26.83,
		SystemPercent: 12.2,
		IOWaitPercent: 34.15,
		StealPercent:  0,
		IdlePercent:   26.83,
		PerCPU: []cpustat.CoreUsage{
			{CPU: 0, UsagePercent: 30},
			{CPU: 1, UsagePercent: 45},
			{CPU: 2, UsagePercent: 35},
			{CPU: 3, UsagePercent: 38.1},
		},
	}
	if got.UsagePercent != want.UsagePercent || got.UserPercent != want.UserPercent ||
		got.SystemPercent != want.SystemPercent || got.IOWaitPercent != want.IOWaitPercent ||
		got.StealPercent != want.StealPercent || got.IdlePercent != want.IdlePercent {
		t.Fatalf("aggregate got %+v\nwant %+v", got, want)
	}
	if len(got.PerCPU) != len(want.PerCPU) {
		t.Fatalf("per_cpu = %+v, want %+v", got.PerCPU, want.PerCPU)
	}
	for i := range want.PerCPU {
		if got.PerCPU[i] != want.PerCPU[i] {
			t.Errorf("per_cpu[%d] = %+v, want %+v", i, got.PerCPU[i], want.PerCPU[i])
		}
	}
}

func TestCollectIdenticalSamplesIsZeroNotNaN(t *testing.T) {
	s := []byte("cpu  10 0 10 100 0 0 0 0 0 0\ncpu0 10 0 10 100 0 0 0 0 0 0\n")
	got, err := cpustat.Collect(context.Background(), &sequenceFS{samples: [][]byte{s, s}}, 0)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.UsagePercent != 0 || got.PerCPU[0].UsagePercent != 0 {
		t.Fatalf("got %+v, want zeros", got)
	}
}

func TestCollectCounterGoingBackwardsIsClamped(t *testing.T) {
	a := []byte("cpu  100 0 100 100 0 0 0 0 0 0\n")
	b := []byte("cpu  50 0 50 200 0 0 0 0 0 0\n") // user/system decreased
	got, err := cpustat.Collect(context.Background(), &sequenceFS{samples: [][]byte{a, b}}, 0)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.UsagePercent != 0 || got.IdlePercent != 100 {
		t.Fatalf("got %+v, want 0%% busy / 100%% idle", got)
	}
}

func TestCollectHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := cpustat.Collect(ctx, fixtureFS(t), time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Collect ignored cancellation and slept")
	}
}

func TestCollectRejectsMalformed(t *testing.T) {
	ok := []byte("cpu  1 2 3 4 5 6 7 8 0 0\n")
	for name, data := range map[string]string{
		"empty":          "",
		"no aggregate":   "cpu0 1 2 3 4 5 6 7 8 0 0\n",
		"too few fields": "cpu  1 2 3\n",
		"not a number":   "cpu  1 2 x 4 5 6 7 8 0 0\n",
		"negative":       "cpu  1 2 -3 4 5 6 7 8 0 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := &sequenceFS{samples: [][]byte{[]byte(data), ok}}
			if _, err := cpustat.Collect(context.Background(), fsys, 0); err == nil {
				t.Fatal("Collect succeeded, want error")
			}
		})
	}
	t.Run("bad core lines are skipped", func(t *testing.T) {
		s := []byte("cpu  1 2 3 4 5 6 7 8 0 0\ncpuX 1 2 3 4 5 6 7 8\ncpu-1 1 2 3 4 5 6 7 8\n")
		got, err := cpustat.Collect(context.Background(), &sequenceFS{samples: [][]byte{s, s}}, 0)
		if err != nil || len(got.PerCPU) != 0 {
			t.Fatalf("got %+v, %v; want no per-cpu entries", got.PerCPU, err)
		}
	})
}
