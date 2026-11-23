package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	sd "github.com/coreos/go-systemd/v22/dbus"
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
	"github.com/xilen0x/linux-diag-mcp/internal/server"
	"github.com/xilen0x/linux-diag-mcp/internal/tools"
)

func fixtureDeps() tools.Deps {
	stat := "cpu  10 0 10 100 0 0 0 0 0 0\ncpu0 10 0 10 100 0 0 0 0 0 0\n"
	return tools.Deps{
		ProcFS: fstest.MapFS{
			"version": {Data: []byte("Linux version 6.1.0-test (x@y) #1 SMP\n")},
			"uptime":  {Data: []byte("123.45 100.00\n")},
			"loadavg": {Data: []byte("0.50 0.25 0.10 2/300 4242\n")},
			"meminfo": {Data: []byte("MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 400 kB\n" +
				"Buffers: 10 kB\nCached: 20 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")},
			"stat":    {Data: []byte(stat)},
			"1/stat":  {Data: []byte("1 (init) S 0 1 1 0 -1 0 0 0 0 0 5 5 0 0 20 0 1 0 15 0 0\n")},
			"1/statm": {Data: []byte("100 25 0 0 0 0 0\n")},
			"2/stat":  {Data: []byte("2 (worker) R 0 1 1 0 -1 0 0 0 0 0 9 9 0 0 20 0 1 0 16 0 0\n")},
			"2/statm": {Data: []byte("100 50 0 0 0 0 0\n")},
			"mounts":  {Data: []byte("/dev/sda1 / ext4 rw 0 0\nproc /proc proc rw 0 0\n")},
			"net/dev": {Data: []byte("h1\nh2\n    lo: 10 1 0 0 0 0 0 0 10 1 0 0 0 0 0 0\n")},
			"net/tcp": {Data: []byte("hdr\n   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 1 1\n")},
			"net/udp": {Data: []byte("hdr\n   0: 0100007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000 0 0 1 1\n")},
		},
		Statfs: func(string) (disk.Stat, error) {
			return disk.Stat{BlockSize: 1024, Blocks: 100, BlocksFree: 50, BlocksAvail: 50}, nil
		},
		EtcFS:             fstest.MapFS{"os-release": {Data: []byte("ID=testos\nNAME=\"Test OS\"\n")}},
		CPUSampleInterval: time.Millisecond,
		PageSize:          4096,
		Systemd:           fakeSystemd,
	}
}

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	srv := server.New(server.Options{Version: "test"})
	tools.Register(srv, fixtureDeps())

	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestToolsList(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		a := tool.Annotations
		if a == nil || !a.ReadOnlyHint || !a.IdempotentHint ||
			a.DestructiveHint == nil || *a.DestructiveHint ||
			a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: annotations %+v, want read-only, idempotent, non-destructive, closed-world", tool.Name, a)
		}
		in, _ := tool.InputSchema.(map[string]any)
		if in["additionalProperties"] != false {
			t.Errorf("%s: input schema allows unknown arguments: %v", tool.Name, in)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s: missing output schema", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s: missing description", tool.Name)
		}
	}
	sort.Strings(names)
	want := []string{"cpu_usage", "disk_usage", "failed_units", "listening_ports", "load_average",
		"memory_usage", "network_interfaces", "service_status", "system_info", "top_processes"}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want %v", names, want)
		}
	}
}

// call invokes a tool and decodes its structured content into out.
func call(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %+v", name, res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshal structured content: %v", name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("%s: decode %s: %v", name, raw, err)
	}
}

func TestCallSystemInfo(t *testing.T) {
	var got sysinfo.Info
	call(t, connect(t), "system_info", nil, &got)
	want := sysinfo.Info{KernelRelease: "6.1.0-test", UptimeSeconds: 123.45,
		OS: sysinfo.OS{ID: "testos", Name: "Test OS"}}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCallLoadAverage(t *testing.T) {
	var got loadavg.LoadAverage
	call(t, connect(t), "load_average", map[string]any{}, &got)
	if got != (loadavg.LoadAverage{Load1: 0.5, Load5: 0.25, Load15: 0.1, Runnable: 2, Total: 300}) {
		t.Fatalf("got %+v", got)
	}
}

func TestCallMemoryUsage(t *testing.T) {
	var got meminfo.Memory
	call(t, connect(t), "memory_usage", nil, &got)
	if got.TotalBytes != 1000*1024 || got.UsedPercent != 60 {
		t.Fatalf("got %+v", got)
	}
}

func TestCallCPUUsage(t *testing.T) {
	var got cpustat.Usage
	call(t, connect(t), "cpu_usage", nil, &got)
	if got.UsagePercent != 0 || len(got.PerCPU) != 1 || got.PerCPU[0].CPU != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestCallRejectsUnknownArguments(t *testing.T) {
	cs := connect(t)
	for _, name := range []string{"system_info", "load_average", "memory_usage", "cpu_usage",
		"disk_usage", "network_interfaces", "failed_units"} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      name,
			Arguments: map[string]any{"path": "/etc/passwd"},
		})
		if err == nil && !res.IsError {
			t.Errorf("%s accepted an unknown argument", name)
		}
	}
}

func TestCollectorErrorIsToolError(t *testing.T) {
	ctx := context.Background()
	srv := server.New(server.Options{Version: "test"})
	tools.Register(srv, tools.Deps{ProcFS: fstest.MapFS{}, EtcFS: fstest.MapFS{}})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "memory_usage"})
	if err != nil {
		t.Fatalf("protocol error, want tool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("missing /proc/meminfo did not produce a tool error")
	}
}

// The SDK serves requests concurrently; run every tool in parallel so the
// race detector can catch shared state between handlers.
func TestConcurrentCalls(t *testing.T) {
	cs := connect(t)
	names := []string{"system_info", "load_average", "memory_usage", "cpu_usage",
		"disk_usage", "network_interfaces", "listening_ports", "top_processes",
		"failed_units", "service_status"}
	errs := make(chan error, 8*len(names))
	for range 8 {
		for _, name := range names {
			go func() {
				var args any
				switch name {
				case "listening_ports":
					args = map[string]any{"protocol": "tcp"}
				case "top_processes":
					args = map[string]any{"sort_by": "cpu"}
				case "service_status":
					args = map[string]any{"unit": "nginx.service"}
				}
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
				if err == nil && res.IsError {
					err = fmt.Errorf("%s: tool error %+v", name, res.Content)
				}
				errs <- err
			}()
		}
	}
	for range 8 * len(names) {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestCallDiskUsage(t *testing.T) {
	var got disk.Usage
	call(t, connect(t), "disk_usage", nil, &got)
	if len(got.Filesystems) != 1 || got.Filesystems[0].MountPoint != "/" || got.Filesystems[0].UsedPercent != 50 {
		t.Fatalf("got %+v", got)
	}
}

func TestCallNetworkInterfaces(t *testing.T) {
	var got netdev.Interfaces
	call(t, connect(t), "network_interfaces", nil, &got)
	if len(got.Interfaces) != 1 || got.Interfaces[0].Name != "lo" || got.Interfaces[0].RxBytes != 10 {
		t.Fatalf("got %+v", got)
	}
}

func TestCallListeningPorts(t *testing.T) {
	cs := connect(t)
	var tcp netsock.Ports
	call(t, cs, "listening_ports", map[string]any{"protocol": "tcp"}, &tcp)
	if tcp.Protocol != "tcp" || len(tcp.Listeners) != 1 || tcp.Listeners[0].Port != 22 {
		t.Fatalf("tcp: got %+v", tcp)
	}
	var udp netsock.Ports
	call(t, cs, "listening_ports", map[string]any{"protocol": "udp"}, &udp)
	if udp.Protocol != "udp" || len(udp.Listeners) != 1 || udp.Listeners[0].Address != "127.0.0.1" {
		t.Fatalf("udp: got %+v", udp)
	}
}

func TestListeningPortsRejectsBadProtocol(t *testing.T) {
	cs := connect(t)
	for _, args := range []map[string]any{
		nil,
		{},
		{"protocol": ""},
		{"protocol": "TCP"},
		{"protocol": "tcp6"},
		{"protocol": "../../etc/passwd"},
		{"protocol": 6},
		{"protocol": []any{"tcp"}},
		{"protocol": "tcp", "extra": true},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "listening_ports", Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("args %v accepted", args)
		}
	}
}

func TestListeningPortsSchemaAdvertisesEnum(t *testing.T) {
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "listening_ports" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		enum := s.Properties["protocol"].Enum
		if len(enum) != 2 || enum[0] != "tcp" || enum[1] != "udp" {
			t.Fatalf("protocol enum = %v, want [tcp udp]; schema %s", enum, raw)
		}
		if len(s.Required) != 1 || s.Required[0] != "protocol" {
			t.Fatalf("required = %v, want [protocol]", s.Required)
		}
		return
	}
	t.Fatal("listening_ports not listed")
}

func TestCallTopProcesses(t *testing.T) {
	cs := connect(t)
	var mem procs.Result
	call(t, cs, "top_processes", map[string]any{"sort_by": "mem", "limit": 1}, &mem)
	if mem.SortBy != "mem" || mem.Limit != 1 || mem.TotalProcesses != 2 ||
		len(mem.Processes) != 1 || mem.Processes[0].PID != 2 || mem.Processes[0].RSSBytes != 50*4096 {
		t.Fatalf("mem: got %+v", mem)
	}
	var def procs.Result
	call(t, cs, "top_processes", map[string]any{"sort_by": "cpu"}, &def)
	if def.Limit != 10 || len(def.Processes) != 2 {
		t.Fatalf("default limit: got %+v", def)
	}
}

func TestTopProcessesRejectsBadArguments(t *testing.T) {
	cs := connect(t)
	for _, args := range []map[string]any{
		nil,
		{},
		{"limit": 5},
		{"sort_by": "CPU"},
		{"sort_by": "rss"},
		{"sort_by": "cpu; kill -9 1"},
		{"sort_by": "cpu", "limit": 0},
		{"sort_by": "cpu", "limit": 21},
		{"sort_by": "cpu", "limit": -1},
		{"sort_by": "cpu", "limit": 2.5},
		{"sort_by": "cpu", "limit": "5"},
		{"sort_by": "cpu", "limit": 1e300},
		{"sort_by": "cpu", "pid": 1},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "top_processes", Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("args %v accepted", args)
		}
	}
}

func TestTopProcessesSchemaAdvertisesConstraints(t *testing.T) {
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "top_processes" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Required   []string `json:"required"`
			Properties struct {
				SortBy struct {
					Enum []string `json:"enum"`
				} `json:"sort_by"`
				Limit struct {
					Minimum     *float64 `json:"minimum"`
					Maximum     *float64 `json:"maximum"`
					Default     *int     `json:"default"`
					Description string   `json:"description"`
				} `json:"limit"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		p := s.Properties
		if len(p.SortBy.Enum) != 2 || p.SortBy.Enum[0] != "cpu" || p.SortBy.Enum[1] != "mem" {
			t.Errorf("sort_by enum = %v; schema %s", p.SortBy.Enum, raw)
		}
		if p.Limit.Minimum == nil || *p.Limit.Minimum != 1 || p.Limit.Maximum == nil || *p.Limit.Maximum != 20 {
			t.Errorf("limit constraints wrong; schema %s", raw)
		}
		// A schema "default" makes the SDK panic on omitted arguments (DoS);
		// the default is documented in the description instead.
		if p.Limit.Default != nil || !strings.Contains(p.Limit.Description, "default 10") {
			t.Errorf("limit must document its default without a schema default; schema %s", raw)
		}
		if len(s.Required) != 1 || s.Required[0] != "sort_by" {
			t.Errorf("required = %v, want [sort_by]", s.Required)
		}
		return
	}
	t.Fatal("top_processes not listed")
}

// busFake is a minimal read-only systemd bus for protocol tests; the
// collector's conversion logic is tested in its own package.
type busFake struct{}

func (busFake) ListUnitsFilteredContext(context.Context, []string) ([]sd.UnitStatus, error) {
	return []sd.UnitStatus{{Name: "backup.service", Description: "Backup", LoadState: "loaded",
		ActiveState: "failed", SubState: "failed"}}, nil
}

func (busFake) ListUnitsByNamesContext(_ context.Context, names []string) ([]sd.UnitStatus, error) {
	if names[0] == "nginx.service" {
		return []sd.UnitStatus{{Name: "nginx.service", LoadState: "loaded", ActiveState: "active", SubState: "running"}}, nil
	}
	return []sd.UnitStatus{{Name: names[0], LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}}, nil
}

func (busFake) GetUnitPropertyContext(context.Context, string, string) (*sd.Property, error) {
	return nil, fmt.Errorf("not available")
}

func (busFake) GetUnitTypePropertiesContext(context.Context, string, string) (map[string]any, error) {
	return map[string]any{"MainPID": uint32(42)}, nil
}

func (busFake) Close() {}

func fakeSystemd(context.Context) (systemd.Conn, error) { return busFake{}, nil }

func TestCallFailedUnits(t *testing.T) {
	var got systemd.Failed
	call(t, connect(t), "failed_units", nil, &got)
	if len(got.Units) != 1 || got.Units[0].Name != "backup.service" || got.Units[0].ActiveState != "failed" {
		t.Fatalf("got %+v", got)
	}
}

func TestCallServiceStatus(t *testing.T) {
	cs := connect(t)
	var got systemd.Service
	call(t, cs, "service_status", map[string]any{"unit": "nginx.service"}, &got)
	if got.Name != "nginx.service" || got.ActiveState != "active" || got.MainPID != 42 {
		t.Fatalf("got %+v", got)
	}
	var ghost systemd.Service
	call(t, cs, "service_status", map[string]any{"unit": "ghost.service"}, &ghost)
	if ghost.LoadState != "not-found" {
		t.Fatalf("ghost: got %+v", ghost)
	}
}

func TestServiceStatusRejectsBadUnit(t *testing.T) {
	cs := connect(t)
	for _, args := range []map[string]any{
		nil,
		{},
		{"unit": ""},
		{"unit": "nginx"},
		{"unit": "nginx.socket"},
		{"unit": "../../etc/passwd"},
		{"unit": "/etc/passwd.service"},
		{"unit": "nginx.service; reboot"},
		{"unit": "nginx.service\n"},
		{"unit": "ｎｇｉｎｘ.service"},
		{"unit": strings.Repeat("a", 129) + ".service"},
		{"unit": 42},
		{"unit": "nginx.service", "action": "restart"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "service_status", Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("args %v accepted", args)
		}
	}
}

func TestServiceStatusSchemaAdvertisesPattern(t *testing.T) {
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "service_status" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Required   []string `json:"required"`
			Properties struct {
				Unit struct {
					Pattern   string `json:"pattern"`
					MaxLength *int   `json:"maxLength"`
				} `json:"unit"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		u := s.Properties.Unit
		if u.Pattern != `^[a-zA-Z0-9@._-]{1,128}\.service$` || u.MaxLength == nil || *u.MaxLength != 136 {
			t.Errorf("unit constraints wrong; schema %s", raw)
		}
		if len(s.Required) != 1 || s.Required[0] != "unit" {
			t.Errorf("required = %v", s.Required)
		}
		return
	}
	t.Fatal("service_status not listed")
}
