// Package tools adapts collectors to MCP tools: schema, validation,
// collector call and structured output. One function per tool.
//
// Every handler error goes through guard.Wrap: only wrapped errors reach
// the client verbatim (see package guard).
package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/cpustat"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/disk"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/loadavg"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/meminfo"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/netdev"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/netsock"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procs"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/sysinfo"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/systemd"
	"github.com/xilen0x/linux-diag-mcp/internal/guard"
	"github.com/xilen0x/linux-diag-mcp/internal/validate"
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
	// Statfs queries mounted filesystems; defaults to disk.SysStatfs when nil.
	Statfs disk.StatFunc
	// PageSize converts statm pages to bytes; defaults to os.Getpagesize().
	PageSize uint64
	// Systemd opens a D-Bus connection; defaults to systemd.DialSystemBus.
	Systemd systemd.Dialer
}

// clockTicks is USER_HZ, the unit of utime/stime in /proc/[pid]/stat. The
// kernel fixes it at 100 for the userspace ABI on all mainstream
// architectures, independently of CONFIG_HZ.
const clockTicks = 100

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
	AddDiskUsage(s, d)
	AddNetworkInterfaces(s, d)
	AddListeningPorts(s, d)
	AddTopProcesses(s, d)
	AddFailedUnits(s, d)
	AddServiceStatus(s, d)
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
		return nil, out, guard.Wrap(err)
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
		return nil, out, guard.Wrap(err)
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
		return nil, out, guard.Wrap(err)
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
		return nil, out, guard.Wrap(err)
	})
}

// AddDiskUsage registers disk_usage.
func AddDiskUsage(s *mcp.Server, d Deps) {
	statfs := d.Statfs
	if statfs == nil {
		statfs = disk.SysStatfs
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "disk_usage",
		Description: "Size, usage and inode usage of mounted real filesystems " +
			"(/proc/mounts + statfs); pseudo filesystems and squashfs images are excluded.",
		Annotations: readOnly("Disk usage"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, disk.Usage, error) {
		out, err := disk.Collect(ctx, d.ProcFS, statfs)
		return nil, out, guard.Wrap(err)
	})
}

// AddNetworkInterfaces registers network_interfaces.
func AddNetworkInterfaces(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "network_interfaces",
		Description: "Per-interface traffic, error and drop counters since boot (/proc/net/dev).",
		Annotations: readOnly("Network interfaces"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, netdev.Interfaces, error) {
		out, err := netdev.Collect(d.ProcFS)
		return nil, out, guard.Wrap(err)
	})
}

// ListeningPortsInput is the argument of listening_ports.
type ListeningPortsInput struct {
	Protocol string `json:"protocol" jsonschema:"transport protocol to inspect"`
}

// listeningPortsSchema adds the enum to the inferred schema so clients can
// see the allowed values; the handler validates again regardless.
func listeningPortsSchema() *jsonschema.Schema {
	sch, err := jsonschema.For[ListeningPortsInput](nil)
	if err != nil {
		panic(fmt.Sprintf("listening_ports schema: %v", err)) // static type: programming error
	}
	sch.Properties["protocol"].Enum = []any{string(validate.ProtocolTCP), string(validate.ProtocolUDP)}
	return sch
}

// AddListeningPorts registers listening_ports.
func AddListeningPorts(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "listening_ports",
		Description: "Local addresses and ports listening for TCP connections or bound UDP sockets, " +
			"IPv4 and IPv6 (/proc/net). Sockets are not mapped to processes.",
		Annotations: readOnly("Listening ports"),
		InputSchema: listeningPortsSchema(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ListeningPortsInput) (*mcp.CallToolResult, netsock.Ports, error) {
		proto, err := validate.ParseProtocol(in.Protocol)
		if err != nil {
			return nil, netsock.Ports{}, guard.Wrap(err)
		}
		out, err := netsock.Collect(d.ProcFS, proto)
		return nil, out, guard.Wrap(err)
	})
}

// TopProcessesInput is the argument of top_processes.
type TopProcessesInput struct {
	SortBy string `json:"sort_by" jsonschema:"ranking key: cpu or mem (resident memory)"`
	Limit  *int   `json:"limit,omitempty" jsonschema:"number of processes to return"`
}

func topProcessesSchema() *jsonschema.Schema {
	sch, err := jsonschema.For[TopProcessesInput](nil)
	if err != nil {
		panic(fmt.Sprintf("top_processes schema: %v", err)) // static type: programming error
	}
	sch.Properties["sort_by"].Enum = []any{string(validate.SortByCPU), string(validate.SortByMem)}
	lo, hi := float64(validate.MinLimit), float64(validate.MaxLimit)
	limit := sch.Properties["limit"]
	limit.Minimum, limit.Maximum = &lo, &hi
	// No schema "default": with it, go-sdk v1.8.0 / jsonschema-go v0.4.3
	// apply defaults into a nil map when arguments are omitted and panic,
	// killing the server (DoS). The default lives in validate.Limit.
	limit.Description = fmt.Sprintf("number of processes to return (default %d)", validate.DefaultLimit)
	return sch
}

// AddTopProcesses registers top_processes.
func AddTopProcesses(s *mcp.Server, d Deps) {
	interval := d.CPUSampleInterval
	if interval <= 0 {
		interval = DefaultCPUSampleInterval
	}
	pageSize := d.PageSize
	if pageSize == 0 {
		pageSize = 4096
		if ps := os.Getpagesize(); ps > 0 {
			pageSize = uint64(ps)
		}
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "top_processes",
		Description: "Top processes by CPU (sampled over a short window) or resident memory. " +
			"Returns only pid, comm, state, cpu_percent and rss_bytes; never command lines or environment.",
		Annotations: readOnly("Top processes"),
		InputSchema: topProcessesSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in TopProcessesInput) (*mcp.CallToolResult, procs.Result, error) {
		sortBy, err := validate.ParseSortKey(in.SortBy)
		if err != nil {
			return nil, procs.Result{}, guard.Wrap(err)
		}
		limit, err := validate.Limit(in.Limit)
		if err != nil {
			return nil, procs.Result{}, guard.Wrap(err)
		}
		out, err := procs.Collect(ctx, d.ProcFS, procs.Options{
			SortBy: sortBy, Limit: limit, Interval: interval, PageSize: pageSize, ClockTicks: clockTicks,
		})
		return nil, out, guard.Wrap(err)
	})
}

func systemdDialer(d Deps) systemd.Dialer {
	if d.Systemd != nil {
		return d.Systemd
	}
	return systemd.DialSystemBus
}

// AddFailedUnits registers failed_units.
func AddFailedUnits(s *mcp.Server, d Deps) {
	dial := systemdDialer(d)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "failed_units",
		Description: "systemd units currently in the failed state (any unit type), via D-Bus.",
		Annotations: readOnly("Failed units"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, systemd.Failed, error) {
		out, err := systemd.FailedUnits(ctx, dial)
		return nil, out, guard.Wrap(err)
	})
}

// ServiceStatusInput is the argument of service_status.
type ServiceStatusInput struct {
	Unit string `json:"unit" jsonschema:"exact systemd service name, e.g. nginx.service"`
}

func serviceStatusSchema() *jsonschema.Schema {
	sch, err := jsonschema.For[ServiceStatusInput](nil)
	if err != nil {
		panic(fmt.Sprintf("service_status schema: %v", err)) // static type: programming error
	}
	maxLen := validate.MaxUnitNameLen
	sch.Properties["unit"].Pattern = validate.UnitNamePattern
	sch.Properties["unit"].MaxLength = &maxLen
	return sch
}

// AddServiceStatus registers service_status.
func AddServiceStatus(s *mcp.Server, d Deps) {
	dial := systemdDialer(d)
	mcp.AddTool(s, &mcp.Tool{
		Name: "service_status",
		Description: "State of one systemd service via D-Bus: load/active/sub state, unit file state, " +
			"main PID, restart count, last result and since when it is active. Unknown names return load_state not-found.",
		Annotations: readOnly("Service status"),
		InputSchema: serviceStatusSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ServiceStatusInput) (*mcp.CallToolResult, systemd.Service, error) {
		unit, err := validate.ParseUnitName(in.Unit)
		if err != nil {
			return nil, systemd.Service{}, guard.Wrap(err)
		}
		out, err := systemd.ServiceStatus(ctx, dial, unit)
		return nil, out, guard.Wrap(err)
	})
}
