// Package cpustat computes CPU utilization from two /proc/stat samples.
package cpustat

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
)

// Usage is the result of cpu_usage. Percentages cover the sampling window.
type Usage struct {
	UsagePercent  float64     `json:"usage_percent" jsonschema:"non-idle time, all CPUs"`
	UserPercent   float64     `json:"user_percent" jsonschema:"user + nice"`
	SystemPercent float64     `json:"system_percent" jsonschema:"system + irq + softirq"`
	IOWaitPercent float64     `json:"iowait_percent"`
	StealPercent  float64     `json:"steal_percent"`
	IdlePercent   float64     `json:"idle_percent"`
	SampleMillis  int64       `json:"sample_ms" jsonschema:"sampling window in milliseconds"`
	PerCPU        []CoreUsage `json:"per_cpu"`
}

// CoreUsage is the utilization of one logical CPU.
type CoreUsage struct {
	CPU          int     `json:"cpu"`
	UsagePercent float64 `json:"usage_percent"`
}

// times holds the jiffy counters of one "cpu" line. guest and guest_nice
// are already included in user and nice, so they are not summed again.
type times struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (t times) total() uint64 {
	return t.user + t.nice + t.system + t.idle + t.iowait + t.irq + t.softirq + t.steal
}

type sample struct {
	all   times
	cores map[int]times
	order []int
}

// Collect reads "stat" from procFS twice, interval apart, and returns the
// utilization in between. It returns ctx.Err() if ctx ends while waiting.
func Collect(ctx context.Context, procFS fs.FS, interval time.Duration) (Usage, error) {
	first, err := read(procFS)
	if err != nil {
		return Usage{}, err
	}
	start := time.Now()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Usage{}, ctx.Err()
	case <-timer.C:
	}
	second, err := read(procFS)
	if err != nil {
		return Usage{}, err
	}

	d := delta(first.all, second.all)
	tot := d.total()
	u := Usage{
		UsagePercent:  procio.Percent(tot-d.idle-d.iowait, tot),
		UserPercent:   procio.Percent(d.user+d.nice, tot),
		SystemPercent: procio.Percent(d.system+d.irq+d.softirq, tot),
		IOWaitPercent: procio.Percent(d.iowait, tot),
		StealPercent:  procio.Percent(d.steal, tot),
		IdlePercent:   procio.Percent(d.idle, tot),
		SampleMillis:  time.Since(start).Milliseconds(),
		PerCPU:        []CoreUsage{},
	}
	for _, id := range second.order {
		before, ok := first.cores[id]
		if !ok { // CPU hot-plugged between samples
			continue
		}
		cd := delta(before, second.cores[id])
		ct := cd.total()
		u.PerCPU = append(u.PerCPU, CoreUsage{CPU: id, UsagePercent: procio.Percent(ct-cd.idle-cd.iowait, ct)})
	}
	return u, nil
}

// delta returns b-a per counter, clamping counters that went backwards
// (wraparound or a CPU coming back online) to zero.
func delta(a, b times) times {
	sub := func(x, y uint64) uint64 {
		if y < x {
			return 0
		}
		return y - x
	}
	return times{
		user: sub(a.user, b.user), nice: sub(a.nice, b.nice), system: sub(a.system, b.system),
		idle: sub(a.idle, b.idle), iowait: sub(a.iowait, b.iowait), irq: sub(a.irq, b.irq),
		softirq: sub(a.softirq, b.softirq), steal: sub(a.steal, b.steal),
	}
}

func read(procFS fs.FS) (sample, error) {
	data, err := procio.ReadFile(procFS, "stat", procio.MaxFileBytes)
	if err != nil {
		return sample{}, err
	}
	s := sample{cores: map[int]times{}}
	found := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), procio.MaxFileBytes)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		if f[0] == "cpu" {
			t, err := parseTimes(f[1:])
			if err != nil {
				return sample{}, fmt.Errorf("parse /proc/stat: %w", err)
			}
			s.all, found = t, true
			continue
		}
		id, err := strconv.ParseUint(f[0][len("cpu"):], 10, 16)
		if err != nil {
			continue // not a "cpuN" line
		}
		t, err := parseTimes(f[1:])
		if err != nil {
			return sample{}, fmt.Errorf("parse /proc/stat: %w", err)
		}
		s.cores[int(id)] = t
		s.order = append(s.order, int(id))
	}
	if err := sc.Err(); err != nil {
		return sample{}, err
	}
	if !found {
		return sample{}, errors.New("parse /proc/stat: missing aggregate cpu line")
	}
	return s, nil
}

func parseTimes(f []string) (times, error) {
	if len(f) < 8 {
		return times{}, errors.New("cpu line has too few fields")
	}
	var v [8]uint64
	for i := range v {
		n, err := strconv.ParseUint(f[i], 10, 64)
		if err != nil {
			return times{}, errors.New("cpu line has a non-numeric field")
		}
		v[i] = n
	}
	return times{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}, nil
}
