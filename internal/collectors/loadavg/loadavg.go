// Package loadavg reads /proc/loadavg.
package loadavg

import (
	"errors"
	"io/fs"
	"math"
	"strconv"
	"strings"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
)

// LoadAverage is the result of load_average.
type LoadAverage struct {
	Load1    float64 `json:"load1" jsonschema:"1-minute load average"`
	Load5    float64 `json:"load5" jsonschema:"5-minute load average"`
	Load15   float64 `json:"load15" jsonschema:"15-minute load average"`
	Runnable int     `json:"runnable_entities" jsonschema:"currently runnable kernel scheduling entities"`
	Total    int     `json:"total_entities" jsonschema:"existing kernel scheduling entities (processes and threads)"`
}

var errFormat = errors.New("parse /proc/loadavg: unexpected format")

// Collect reads "loadavg" from procFS (rooted at /proc).
func Collect(procFS fs.FS) (LoadAverage, error) {
	data, err := procio.ReadFile(procFS, "loadavg", 4096)
	if err != nil {
		return LoadAverage{}, err
	}
	f := strings.Fields(string(data))
	if len(f) < 4 {
		return LoadAverage{}, errFormat
	}
	var loads [3]float64
	for i := range loads {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return LoadAverage{}, errFormat
		}
		loads[i] = v
	}
	run, total, ok := strings.Cut(f[3], "/")
	if !ok {
		return LoadAverage{}, errFormat
	}
	r, err1 := strconv.ParseUint(run, 10, 31)
	t, err2 := strconv.ParseUint(total, 10, 31)
	if err1 != nil || err2 != nil {
		return LoadAverage{}, errFormat
	}
	return LoadAverage{Load1: loads[0], Load5: loads[1], Load15: loads[2], Runnable: int(r), Total: int(t)}, nil
}
