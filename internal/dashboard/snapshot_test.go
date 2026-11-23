package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeCaller answers every tool with {"tool": name} unless a handler overrides it.
type fakeCaller struct {
	mu       sync.Mutex
	calls    []string
	args     map[string]any
	handlers map[string]func(args any) (*mcp.CallToolResult, error)
}

func (f *fakeCaller) CallTool(ctx context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, p.Name)
	if f.args == nil {
		f.args = map[string]any{}
	}
	key := p.Name
	if m, ok := p.Arguments.(map[string]any); ok {
		if v, ok := m["protocol"]; ok {
			key += "_" + v.(string)
		}
		if v, ok := m["sort_by"]; ok {
			key += "_" + v.(string)
		}
		if v, ok := m["unit"]; ok {
			key += "_" + v.(string)
		}
	}
	f.args[key] = p.Arguments
	h := f.handlers[p.Name]
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h != nil {
		return h(p.Arguments)
	}
	return &mcp.CallToolResult{StructuredContent: map[string]any{"tool": p.Name}}, nil
}

func (f *fakeCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestCollector(caller Caller, maxAge time.Duration) (*Collector, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 11, 23, 21, 0, 0, 0, time.UTC)}
	c := NewCollector(caller, maxAge)
	c.now = clk.now
	return c, clk
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestSnapshotCallsEveryToolWithFixedArguments(t *testing.T) {
	f := &fakeCaller{}
	c, clk := newTestCollector(f, time.Second)

	s := c.Snapshot(context.Background())

	wantKeys := []string{
		"cpu_usage", "disk_usage", "failed_units", "listening_ports_tcp", "listening_ports_udp",
		"load_average", "memory_usage", "network_interfaces", "system_info",
		"top_processes_cpu", "top_processes_mem",
	}
	if got := keys(s.Tools); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("tool keys = %v, want %v", got, wantKeys)
	}
	if len(s.Errors) != 0 {
		t.Fatalf("errors = %v, want none", s.Errors)
	}
	if !s.CapturedAt.Equal(clk.now()) {
		t.Fatalf("captured_at = %v, want %v", s.CapturedAt, clk.now())
	}
	var body map[string]string
	if err := json.Unmarshal(s.Tools["memory_usage"], &body); err != nil || body["tool"] != "memory_usage" {
		t.Fatalf("memory_usage payload = %s (%v)", s.Tools["memory_usage"], err)
	}

	wantArgs := map[string]any{
		"listening_ports_tcp": map[string]any{"protocol": "tcp"},
		"listening_ports_udp": map[string]any{"protocol": "udp"},
		"top_processes_cpu":   map[string]any{"sort_by": "cpu", "limit": TopProcessesLimit},
		"top_processes_mem":   map[string]any{"sort_by": "mem", "limit": TopProcessesLimit},
	}
	for k, want := range wantArgs {
		if got := f.args[k]; !reflect.DeepEqual(got, want) {
			t.Errorf("args[%s] = %#v, want %#v", k, got, want)
		}
	}
}

func TestSnapshotQueriesServiceStatusOnlyForFailedServices(t *testing.T) {
	f := &fakeCaller{handlers: map[string]func(any) (*mcp.CallToolResult, error){
		"failed_units": func(any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: map[string]any{"units": []any{
				map[string]any{"name": "apparmor.service"},
				map[string]any{"name": "mnt-nfs.mount"},
				map[string]any{"name": "openipmi.service"},
			}}}, nil
		},
		"service_status": func(a any) (*mcp.CallToolResult, error) {
			unit := a.(map[string]any)["unit"].(string)
			return &mcp.CallToolResult{StructuredContent: map[string]any{"name": unit}}, nil
		},
	}}
	c, _ := newTestCollector(f, time.Second)

	s := c.Snapshot(context.Background())

	if got, want := keys(s.Services), []string{"apparmor.service", "openipmi.service"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
	if _, called := f.args["service_status_mnt-nfs.mount"]; called {
		t.Fatal("service_status was called for a .mount unit")
	}
}

func TestSnapshotCapsServiceStatusCalls(t *testing.T) {
	units := make([]any, 0, MaxServiceStatus+5)
	for i := range MaxServiceStatus + 5 {
		units = append(units, map[string]any{"name": "u" + string(rune('a'+i)) + ".service"})
	}
	f := &fakeCaller{handlers: map[string]func(any) (*mcp.CallToolResult, error){
		"failed_units": func(any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: map[string]any{"units": units}}, nil
		},
	}}
	c, _ := newTestCollector(f, time.Second)

	s := c.Snapshot(context.Background())

	if len(s.Services) != MaxServiceStatus {
		t.Fatalf("services = %d, want %d", len(s.Services), MaxServiceStatus)
	}
}

func TestSnapshotRecordsPerToolErrorsAndKeepsTheRest(t *testing.T) {
	f := &fakeCaller{handlers: map[string]func(any) (*mcp.CallToolResult, error){
		"disk_usage": func(any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "tool timed out"}}}, nil
		},
		"cpu_usage": func(any) (*mcp.CallToolResult, error) {
			return nil, errors.New("connection closed: secret detail")
		},
		"system_info": func(any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	}}
	c, _ := newTestCollector(f, time.Second)

	s := c.Snapshot(context.Background())

	want := map[string]string{
		"disk_usage":  "tool timed out",
		"cpu_usage":   ErrCallFailed,
		"system_info": ErrNoData,
	}
	if !reflect.DeepEqual(s.Errors, want) {
		t.Fatalf("errors = %v, want %v", s.Errors, want)
	}
	for k := range want {
		if _, ok := s.Tools[k]; ok {
			t.Errorf("tools[%s] present despite error", k)
		}
	}
	if _, ok := s.Tools["memory_usage"]; !ok {
		t.Error("memory_usage missing: one failing tool must not drop the others")
	}
}

func TestSnapshotTruncatesLongToolErrors(t *testing.T) {
	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'x'
	}
	f := &fakeCaller{handlers: map[string]func(any) (*mcp.CallToolResult, error){
		"load_average": func(any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(long)}}}, nil
		},
	}}
	c, _ := newTestCollector(f, time.Second)

	s := c.Snapshot(context.Background())

	if n := len([]rune(s.Errors["load_average"])); n != maxErrorRunes {
		t.Fatalf("error length = %d, want %d", n, maxErrorRunes)
	}
}

func TestSnapshotIsCachedForMaxAge(t *testing.T) {
	f := &fakeCaller{}
	c, clk := newTestCollector(f, 5*time.Second)

	first := c.Snapshot(context.Background())
	perRound := f.callCount()

	clk.advance(4 * time.Second)
	if again := c.Snapshot(context.Background()); again != first {
		t.Fatal("snapshot within max age was not reused")
	}
	if f.callCount() != perRound {
		t.Fatalf("calls = %d, want %d (cached)", f.callCount(), perRound)
	}

	clk.advance(2 * time.Second)
	if fresh := c.Snapshot(context.Background()); fresh == first {
		t.Fatal("snapshot older than max age was reused")
	}
	if f.callCount() != 2*perRound {
		t.Fatalf("calls = %d, want %d (refreshed)", f.callCount(), 2*perRound)
	}
}

func TestSnapshotConcurrentRequestsCollectOnce(t *testing.T) {
	f := &fakeCaller{}
	c, _ := newTestCollector(f, time.Minute)
	c.Snapshot(context.Background())
	perRound := f.callCount()
	c.last = nil // force one fresh collection shared by all goroutines

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { c.Snapshot(context.Background()) })
	}
	wg.Wait()

	if got := f.callCount(); got != 2*perRound {
		t.Fatalf("calls = %d, want %d (one collection for 16 requests)", got, 2*perRound)
	}
}

func TestSnapshotSurvivesCanceledRequestContext(t *testing.T) {
	f := &fakeCaller{}
	c, _ := newTestCollector(f, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := c.Snapshot(ctx)

	if len(s.Errors) != 0 {
		t.Fatalf("errors = %v: a client that left must not poison the shared snapshot", s.Errors)
	}
}
