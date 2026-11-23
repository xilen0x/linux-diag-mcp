package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"testing/fstest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/cpustat"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/disk"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/loadavg"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/meminfo"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/netdev"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/netsock"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/sysinfo"
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
	want := []string{"cpu_usage", "disk_usage", "listening_ports", "load_average",
		"memory_usage", "network_interfaces", "system_info"}
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
		"disk_usage", "network_interfaces"} {
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
		"disk_usage", "network_interfaces", "listening_ports"}
	errs := make(chan error, 8*len(names))
	for range 8 {
		for _, name := range names {
			go func() {
				var args any
				if name == "listening_ports" {
					args = map[string]any{"protocol": "tcp"}
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
