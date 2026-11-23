// Package tools adapts collectors to MCP tools: schema, validation,
// collector call and structured output. One function per tool.
package tools

import (
	"context"
	"io/fs"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/cpustat"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/loadavg"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/meminfo"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/sysinfo"
)

// DefaultCPUSampleInterval is the gap between the two /proc/stat samples.
const DefaultCPUSampleInterval = 200 * time.Millisecond

// Deps are the data sources the tools read from. Each tool receives them
// explicitly; there is no package-level state.
type Deps struct {
	// ProcFS is rooted at /proc.
	ProcFS fs.FS
	// EtcFS is rooted at /etc (only os-release is read).
	EtcFS fs.FS
	// CPUSampleInterval defaults to DefaultCPUSampleInterval when zero.
	CPUSampleInterval time.Duration
}

// NoInput is the argument type of tools without parameters. As a struct it
// infers a schema with additionalProperties=false, so any argument is
// rejected by the SDK before the handler runs.
type NoInput struct{}

// Register adds every tool to s.
func Register(s *mcp.Server, d Deps) {
	AddSystemInfo(s, d)
	AddLoadAverage(s, d)
	AddMemoryUsage(s, d)
	AddCPUUsage(s, d)
}

// readOnly returns fresh annotations per tool (no shared mutable pointers).
func readOnly(title string) *mcp.ToolAnnotations {
	f := false
	closed := false
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		DestructiveHint: &f,
		OpenWorldHint:   &closed,
	}
}

// AddSystemInfo registers system_info.
func AddSystemInfo(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "system_info",
		Description: "Kernel release, uptime and OS identification (/proc/version, /proc/uptime, /etc/os-release).",
		Annotations: readOnly("System information"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, sysinfo.Info, error) {
		out, err := sysinfo.Collect(d.ProcFS, d.EtcFS)
		return nil, out, err
	})
}

// AddLoadAverage registers load_average.
func AddLoadAverage(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "load_average",
		Description: "1, 5 and 15 minute load averages and scheduling entity counts (/proc/loadavg).",
		Annotations: readOnly("Load average"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, loadavg.LoadAverage, error) {
		out, err := loadavg.Collect(d.ProcFS)
		return nil, out, err
	})
}

// AddMemoryUsage registers memory_usage.
func AddMemoryUsage(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "memory_usage",
		Description: "RAM and swap totals and usage in bytes (/proc/meminfo).",
		Annotations: readOnly("Memory usage"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, meminfo.Memory, error) {
		out, err := meminfo.Collect(d.ProcFS)
		return nil, out, err
	})
}

// AddCPUUsage registers cpu_usage.
func AddCPUUsage(s *mcp.Server, d Deps) {
	interval := d.CPUSampleInterval
	if interval <= 0 {
		interval = DefaultCPUSampleInterval
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "cpu_usage",
		Description: "CPU utilization, overall and per logical CPU, measured over a short sampling window (/proc/stat).",
		Annotations: readOnly("CPU usage"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, cpustat.Usage, error) {
		out, err := cpustat.Collect(ctx, d.ProcFS, interval)
		return nil, out, err
	})
}
