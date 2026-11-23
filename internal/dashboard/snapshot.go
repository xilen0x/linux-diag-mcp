// Package dashboard serves the diagnostics tools as a live web page.
//
// Data still flows through the MCP server (in-process, over in-memory
// transports), so every call keeps the guard's timeout, truncation and
// panic recovery. This package only collects, caches and serves it.
package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// TopProcessesLimit is how many processes each top_processes call returns.
	TopProcessesLimit = 10
	// MaxServiceStatus bounds the service_status calls made per snapshot.
	MaxServiceStatus = 20
	// collectTimeout bounds one whole collection, whatever the request does.
	collectTimeout = 30 * time.Second
	// maxErrorRunes bounds a tool error message copied into the snapshot.
	maxErrorRunes = 200
)

// Constant messages for failures that carry no safe text of their own.
const (
	ErrCallFailed = "call failed"
	ErrNoData     = "no data"
)

// Caller is the part of *mcp.ClientSession the collector uses.
type Caller interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

// Snapshot is one reading of every tool, as served to the page.
type Snapshot struct {
	CapturedAt time.Time                  `json:"captured_at"`
	Tools      map[string]json.RawMessage `json:"tools"`
	Services   map[string]json.RawMessage `json:"services"`
	Errors     map[string]string          `json:"errors"`
}

type toolCall struct {
	key  string
	name string
	args map[string]any
}

// calls is the fixed set of tool calls in one snapshot. Arguments are
// constants: nothing a browser sends ever reaches a tool.
var calls = []toolCall{
	{"system_info", "system_info", nil},
	{"load_average", "load_average", nil},
	{"memory_usage", "memory_usage", nil},
	{"cpu_usage", "cpu_usage", nil},
	{"disk_usage", "disk_usage", nil},
	{"network_interfaces", "network_interfaces", nil},
	{"listening_ports_tcp", "listening_ports", map[string]any{"protocol": "tcp"}},
	{"listening_ports_udp", "listening_ports", map[string]any{"protocol": "udp"}},
	{"top_processes_cpu", "top_processes", map[string]any{"sort_by": "cpu", "limit": TopProcessesLimit}},
	{"top_processes_mem", "top_processes", map[string]any{"sort_by": "mem", "limit": TopProcessesLimit}},
	{"failed_units", "failed_units", nil},
}

// Collector takes snapshots and reuses the last one while it is younger
// than maxAge, so any number of open pages costs one collection per period.
type Collector struct {
	caller Caller
	maxAge time.Duration
	now    func() time.Time

	mu   sync.Mutex
	last *Snapshot
}

// NewCollector returns a collector that calls tools through caller.
func NewCollector(caller Caller, maxAge time.Duration) *Collector {
	return &Collector{caller: caller, maxAge: maxAge, now: time.Now}
}

// Snapshot returns the cached snapshot, or collects a new one when it is
// older than maxAge. Concurrent callers wait for a single collection.
// The returned value is shared and must not be modified.
func (c *Collector) Snapshot(ctx context.Context) *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last != nil && c.now().Sub(c.last.CapturedAt) < c.maxAge {
		return c.last
	}
	// The snapshot is shared by every page, so a request that goes away
	// must not cancel the collection it started.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), collectTimeout)
	defer cancel()
	c.last = c.collect(cctx)
	return c.last
}

func (c *Collector) collect(ctx context.Context) *Snapshot {
	s := &Snapshot{
		CapturedAt: c.now(),
		Tools:      map[string]json.RawMessage{},
		Services:   map[string]json.RawMessage{},
		Errors:     map[string]string{},
	}
	for _, tc := range calls {
		if raw, msg := c.call(ctx, tc.name, tc.args); msg != "" {
			s.Errors[tc.key] = msg
		} else {
			s.Tools[tc.key] = raw
		}
	}
	for _, unit := range failedServices(s.Tools["failed_units"]) {
		key := "service_status:" + unit
		if raw, msg := c.call(ctx, "service_status", map[string]any{"unit": unit}); msg != "" {
			s.Errors[key] = msg
		} else {
			s.Services[unit] = raw
		}
	}
	return s
}

// call runs one tool and returns its structured output, or a message
// explaining why there is none.
func (c *Collector) call(ctx context.Context, name string, args map[string]any) (json.RawMessage, string) {
	params := &mcp.CallToolParams{Name: name}
	if args != nil {
		params.Arguments = args
	}
	res, err := c.caller.CallTool(ctx, params)
	if err != nil {
		// Transport errors can carry internal detail; keep them off the page.
		return nil, ErrCallFailed
	}
	if res.IsError {
		return nil, errorText(res)
	}
	if res.StructuredContent == nil {
		return nil, ErrNoData
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return nil, ErrNoData
	}
	return raw, ""
}

// errorText returns a tool error's text. The guard already turns every
// tool error into a constant message; this only bounds its length.
func errorText(res *mcp.CallToolResult) string {
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok && tc.Text != "" {
			r := []rune(tc.Text)
			if len(r) > maxErrorRunes {
				r = r[:maxErrorRunes]
			}
			return string(r)
		}
	}
	return ErrCallFailed
}

// failedServices lists the failed .service units, up to MaxServiceStatus.
// service_status only accepts .service names.
func failedServices(raw json.RawMessage) []string {
	if raw == nil {
		return nil
	}
	var out struct {
		Units []struct {
			Name string `json:"name"`
		} `json:"units"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	var names []string
	for _, u := range out.Units {
		if strings.HasSuffix(u.Name, ".service") {
			names = append(names, u.Name)
			if len(names) == MaxServiceStatus {
				break
			}
		}
	}
	return names
}
