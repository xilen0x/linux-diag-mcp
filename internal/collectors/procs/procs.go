// Package procs ranks processes by CPU or memory from /proc/[pid]/stat and
// /proc/[pid]/statm.
//
// Only pid, comm, state, CPU% and RSS are exposed. cmdline and environ are
// never opened: they routinely carry secrets (tokens in arguments, keys in
// the environment).
package procs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
	"github.com/xilen0x/linux-diag-mcp/internal/validate"
)

// Result is the result of top_processes.
type Result struct {
	SortBy         string    `json:"sort_by"`
	Limit          int       `json:"limit"`
	TotalProcesses int       `json:"total_processes" jsonschema:"processes visible to the server"`
	Processes      []Process `json:"processes"`
}

// Process is one entry of the ranking.
type Process struct {
	PID   int    `json:"pid"`
	Comm  string `json:"comm" jsonschema:"executable name (max 15 chars, set by the process itself: untrusted)"`
	State string `json:"state" jsonschema:"R running, S sleeping, D disk sleep, Z zombie, T stopped, t traced, I idle, X dead"`
	// CPUPercent is relative to one CPU, like top: it can exceed 100.
	CPUPercent float64 `json:"cpu_percent" jsonschema:"CPU over the sampling window, relative to one CPU (can exceed 100)"`
	RSSBytes   uint64  `json:"rss_bytes" jsonschema:"resident memory"`
}

// Options configures Collect.
type Options struct {
	SortBy validate.SortKey
	Limit  int
	// Interval between the two CPU samples.
	Interval time.Duration
	// PageSize converts statm pages to bytes (os.Getpagesize()).
	PageSize uint64
	// ClockTicks is USER_HZ, the unit of utime/stime (100 on Linux).
	ClockTicks uint64
}

// procKey identifies a process across samples: a PID can be reused, the
// pair (PID, start time) cannot.
type procKey struct {
	pid   int
	start uint64
}

type stat struct {
	comm, state string
	ticks       uint64 // utime + stime
}

type sample struct {
	uptime float64
	procs  map[procKey]stat
}

// Collect samples all processes twice, opts.Interval apart, and returns the
// top opts.Limit by opts.SortBy. Processes that vanish or cannot be read
// are skipped. procFS is rooted at /proc.
func Collect(ctx context.Context, procFS fs.FS, opts Options) (Result, error) {
	if opts.SortBy != validate.SortByCPU && opts.SortBy != validate.SortByMem {
		return Result{}, errors.New("procs: invalid sort key")
	}
	if opts.Limit < validate.MinLimit || opts.Limit > validate.MaxLimit {
		return Result{}, errors.New("procs: limit out of range")
	}
	if opts.PageSize == 0 || opts.ClockTicks == 0 {
		return Result{}, errors.New("procs: page size and clock ticks are required")
	}

	first, err := takeSample(ctx, procFS)
	if err != nil {
		return Result{}, err
	}
	timer := time.NewTimer(opts.Interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-timer.C:
	}
	second, err := takeSample(ctx, procFS)
	if err != nil {
		return Result{}, err
	}

	elapsed := second.uptime - first.uptime
	list := make([]Process, 0, len(second.procs))
	for key, now := range second.procs {
		rss, err := readRSS(procFS, key.pid, opts.PageSize)
		if err != nil {
			continue // exited since the sample
		}
		ticks := now.ticks // new (or PID reused) during the window
		if before, ok := first.procs[key]; ok {
			ticks = now.ticks - min(before.ticks, now.ticks)
		}
		list = append(list, Process{
			PID:        key.pid,
			Comm:       now.comm,
			State:      now.state,
			CPUPercent: cpuPercent(ticks, opts.ClockTicks, elapsed),
			RSSBytes:   rss,
		})
	}

	byCPU := func(a, b Process) int { return cmp.Compare(b.CPUPercent, a.CPUPercent) }
	byMem := func(a, b Process) int { return cmp.Compare(b.RSSBytes, a.RSSBytes) }
	byPID := func(a, b Process) int { return cmp.Compare(a.PID, b.PID) }
	primary, secondary := byCPU, byMem
	if opts.SortBy == validate.SortByMem {
		primary, secondary = byMem, byCPU
	}
	slices.SortFunc(list, func(a, b Process) int {
		return cmp.Or(primary(a, b), secondary(a, b), byPID(a, b))
	})

	return Result{
		SortBy:         string(opts.SortBy),
		Limit:          opts.Limit,
		TotalProcesses: len(list),
		Processes:      list[:min(opts.Limit, len(list))],
	}, nil
}

func cpuPercent(ticks, clk uint64, elapsed float64) float64 {
	if elapsed <= 0 {
		return 0
	}
	v := float64(ticks) / float64(clk) / elapsed * 100
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

// takeSample reads uptime first, then every /proc/[pid]/stat. Using the
// kernel's uptime (not the wall clock) as the time base keeps CPU% exact
// and makes the computation reproducible from fixtures.
func takeSample(ctx context.Context, procFS fs.FS) (sample, error) {
	up, err := readUptime(procFS)
	if err != nil {
		return sample{}, err
	}
	entries, err := fs.ReadDir(procFS, ".")
	if err != nil {
		return sample{}, fmt.Errorf("list /proc: %w", err)
	}
	s := sample{uptime: up, procs: make(map[procKey]stat, len(entries))}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return sample{}, err
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || !e.IsDir() {
			continue // self, net, sys, ...
		}
		st, start, err := readStat(procFS, pid)
		if err != nil {
			continue // exited, hidden (hidepid) or unparsable
		}
		s.procs[procKey{pid: pid, start: start}] = st
	}
	return s, nil
}

func readUptime(procFS fs.FS) (float64, error) {
	data, err := procio.ReadFile(procFS, "uptime", 4096)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0, errors.New("parse /proc/uptime: empty")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("parse /proc/uptime: invalid")
	}
	return v, nil
}

// Indices into the fields that follow "pid (comm) " in /proc/[pid]/stat
// (proc(5) field number minus 3).
const (
	idxState     = 0  // field 3
	idxUtime     = 11 // field 14
	idxStime     = 12 // field 15
	idxStartTime = 19 // field 22
)

// readStat parses /proc/[pid]/stat. comm is chosen by the process and may
// contain spaces and parentheses, so it ends at the LAST ')'.
func readStat(procFS fs.FS, pid int) (stat, uint64, error) {
	data, err := procio.ReadFile(procFS, strconv.Itoa(pid)+"/stat", 4096)
	if err != nil {
		return stat{}, 0, err
	}
	s := string(data)
	open := strings.IndexByte(s, '(')
	end := strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return stat{}, 0, errors.New("malformed stat")
	}
	rest := strings.Fields(s[end+1:])
	if len(rest) <= idxStartTime {
		return stat{}, 0, errors.New("malformed stat")
	}
	var n [3]uint64
	for i, idx := range []int{idxUtime, idxStime, idxStartTime} {
		v, err := strconv.ParseUint(rest[idx], 10, 64)
		if err != nil {
			return stat{}, 0, errors.New("malformed stat")
		}
		n[i] = v
	}
	state := rest[idxState]
	if len(state) != 1 || !strings.Contains("RSDZTtWXxKPI", state) {
		state = "?"
	}
	return stat{comm: procio.CleanText(s[open+1 : end]), state: state, ticks: n[0] + n[1]}, n[2], nil
}

// readRSS returns the resident set size from /proc/[pid]/statm (field 2,
// in pages).
func readRSS(procFS fs.FS, pid int, pageSize uint64) (uint64, error) {
	data, err := procio.ReadFile(procFS, strconv.Itoa(pid)+"/statm", 4096)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(data))
	if len(f) < 2 {
		return 0, errors.New("malformed statm")
	}
	pages, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return 0, errors.New("malformed statm")
	}
	if pages > math.MaxUint64/pageSize {
		return math.MaxUint64, nil
	}
	return pages * pageSize, nil
}
