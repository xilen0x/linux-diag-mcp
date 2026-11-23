// Package meminfo reads /proc/meminfo.
package meminfo

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"math"
	"strconv"
	"strings"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
)

// Memory is the result of memory_usage. All sizes are in bytes.
type Memory struct {
	TotalBytes      uint64  `json:"total_bytes"`
	FreeBytes       uint64  `json:"free_bytes" jsonschema:"completely unused memory"`
	AvailableBytes  uint64  `json:"available_bytes" jsonschema:"memory available for new workloads without swapping"`
	BuffersBytes    uint64  `json:"buffers_bytes"`
	CachedBytes     uint64  `json:"cached_bytes"`
	UsedBytes       uint64  `json:"used_bytes" jsonschema:"total minus available"`
	UsedPercent     float64 `json:"used_percent"`
	SwapTotalBytes  uint64  `json:"swap_total_bytes"`
	SwapFreeBytes   uint64  `json:"swap_free_bytes"`
	SwapUsedBytes   uint64  `json:"swap_used_bytes"`
	SwapUsedPercent float64 `json:"swap_used_percent"`
}

// Collect reads "meminfo" from procFS (rooted at /proc).
func Collect(procFS fs.FS) (Memory, error) {
	data, err := procio.ReadFile(procFS, "meminfo", 64*1024)
	if err != nil {
		return Memory{}, err
	}
	required := []string{"MemTotal", "MemFree", "MemAvailable", "Buffers", "Cached", "SwapTotal", "SwapFree"}
	want := make(map[string]bool, len(required))
	for _, k := range required {
		want[k] = true
	}
	v := make(map[string]uint64, len(required))
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || !want[key] {
			continue
		}
		n, err := parseKB(rest)
		if err != nil {
			return Memory{}, fmt.Errorf("parse /proc/meminfo %s: %w", key, err)
		}
		v[key] = n
	}
	if err := sc.Err(); err != nil {
		return Memory{}, err
	}
	for _, k := range required {
		if _, ok := v[k]; !ok {
			return Memory{}, fmt.Errorf("parse /proc/meminfo: missing %s", k)
		}
	}
	m := Memory{
		TotalBytes:     v["MemTotal"],
		FreeBytes:      v["MemFree"],
		AvailableBytes: v["MemAvailable"],
		BuffersBytes:   v["Buffers"],
		CachedBytes:    v["Cached"],
		SwapTotalBytes: v["SwapTotal"],
		SwapFreeBytes:  v["SwapFree"],
	}
	if m.AvailableBytes > m.TotalBytes || m.SwapFreeBytes > m.SwapTotalBytes {
		return Memory{}, fmt.Errorf("parse /proc/meminfo: inconsistent values")
	}
	m.UsedBytes = m.TotalBytes - m.AvailableBytes
	m.UsedPercent = procio.Percent(m.UsedBytes, m.TotalBytes)
	m.SwapUsedBytes = m.SwapTotalBytes - m.SwapFreeBytes
	m.SwapUsedPercent = procio.Percent(m.SwapUsedBytes, m.SwapTotalBytes)
	return m, nil
}

// parseKB parses "   16239724 kB" into bytes, rejecting overflow.
func parseKB(s string) (uint64, error) {
	f := strings.Fields(s)
	if len(f) != 2 || f[1] != "kB" {
		return 0, fmt.Errorf("unexpected format")
	}
	n, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number")
	}
	if n > math.MaxUint64/1024 {
		return 0, fmt.Errorf("value overflows")
	}
	return n * 1024, nil
}
