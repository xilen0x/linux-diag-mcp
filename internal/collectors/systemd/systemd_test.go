package systemd_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	sd "github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/systemd"
	"github.com/xilen0x/linux-diag-mcp/internal/validate"
)

// fakeConn mimics systemd's D-Bus API, including its loosely typed
// property maps, and records calls.
type fakeConn struct {
	mu        sync.Mutex
	units     map[string]sd.UnitStatus
	unitProps map[string]map[string]any // unit -> property -> value
	svcProps  map[string]map[string]any
	err       error
	calls     []string
	closed    bool
}

func (f *fakeConn) record(c string) { f.mu.Lock(); f.calls = append(f.calls, c); f.mu.Unlock() }

func (f *fakeConn) ListUnitsFilteredContext(_ context.Context, states []string) ([]sd.UnitStatus, error) {
	f.record("ListUnitsFiltered")
	if f.err != nil {
		return nil, f.err
	}
	var out []sd.UnitStatus
	for _, u := range f.units {
		if slices.Contains(states, u.ActiveState) {
			out = append(out, u)
		}
	}
	// Reverse order: the collector must sort, not rely on the bus.
	slices.SortFunc(out, func(a, b sd.UnitStatus) int { return compare(b.Name, a.Name) })
	return out, nil
}

func (f *fakeConn) ListUnitsByNamesContext(_ context.Context, names []string) ([]sd.UnitStatus, error) {
	f.record("ListUnitsByNames:" + names[0])
	if f.err != nil {
		return nil, f.err
	}
	var out []sd.UnitStatus
	for _, n := range names {
		u, ok := f.units[n]
		if !ok { // systemd reports unknown names as not-found, not as an error
			u = sd.UnitStatus{Name: n, LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}
		}
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeConn) GetUnitPropertyContext(_ context.Context, unit, name string) (*sd.Property, error) {
	f.record("GetUnitProperty:" + name)
	v, ok := f.unitProps[unit][name]
	if !ok {
		return nil, errors.New("no such property")
	}
	return &sd.Property{Name: name, Value: godbus.MakeVariant(v)}, nil
}

func (f *fakeConn) GetUnitTypePropertiesContext(_ context.Context, unit, unitType string) (map[string]any, error) {
	f.record("GetUnitTypeProperties:" + unitType)
	if p, ok := f.svcProps[unit]; ok {
		return p, nil
	}
	return nil, errors.New("unknown interface")
}

func (f *fakeConn) Close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func dialer(c *fakeConn) systemd.Dialer {
	return func(context.Context) (systemd.Conn, error) { return c, nil }
}

func newFake() *fakeConn {
	return &fakeConn{
		units: map[string]sd.UnitStatus{
			"nginx.service": {Name: "nginx.service", Description: "A high performance web server",
				LoadState: "loaded", ActiveState: "active", SubState: "running"},
			"backup.service": {Name: "backup.service", Description: "Nightly \x1b[31mbackup\x1b[0m",
				LoadState: "loaded", ActiveState: "failed", SubState: "failed"},
			"dead.mount": {Name: "dead.mount", Description: "Broken mount",
				LoadState: "loaded", ActiveState: "failed", SubState: "failed"},
		},
		unitProps: map[string]map[string]any{
			// 2026-11-23T12:00:00Z in microseconds since the epoch.
			"nginx.service":  {"UnitFileState": "enabled", "ActiveEnterTimestamp": uint64(1795435200000000)},
			"backup.service": {"UnitFileState": "static", "ActiveEnterTimestamp": uint64(0)},
		},
		svcProps: map[string]map[string]any{
			"nginx.service":  {"MainPID": uint32(1234), "NRestarts": uint32(2), "Result": "success"},
			"backup.service": {"MainPID": uint32(0), "NRestarts": "not-a-number", "Result": "exit-code"},
		},
	}
}

func mustUnit(t *testing.T, s string) validate.UnitName {
	t.Helper()
	u, err := validate.ParseUnitName(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestFailedUnits(t *testing.T) {
	fake := newFake()
	got, err := systemd.FailedUnits(context.Background(), dialer(fake))
	if err != nil {
		t.Fatalf("FailedUnits: %v", err)
	}
	want := []systemd.Unit{
		{Name: "backup.service", Description: "Nightly [31mbackup[0m", LoadState: "loaded", ActiveState: "failed", SubState: "failed"},
		{Name: "dead.mount", Description: "Broken mount", LoadState: "loaded", ActiveState: "failed", SubState: "failed"},
	}
	if !slices.Equal(got.Units, want) {
		t.Fatalf("got  %+v\nwant %+v", got.Units, want)
	}
	if !fake.closed {
		t.Error("connection not closed")
	}
}

func TestFailedUnitsNoneIsEmptyList(t *testing.T) {
	fake := &fakeConn{units: map[string]sd.UnitStatus{}}
	got, err := systemd.FailedUnits(context.Background(), dialer(fake))
	if err != nil || got.Units == nil || len(got.Units) != 0 {
		t.Fatalf("got %+v, %v; want non-nil empty list", got, err)
	}
}

func TestServiceStatusRunning(t *testing.T) {
	fake := newFake()
	got, err := systemd.ServiceStatus(context.Background(), dialer(fake), mustUnit(t, "nginx.service"))
	if err != nil {
		t.Fatalf("ServiceStatus: %v", err)
	}
	want := systemd.Service{
		Unit: systemd.Unit{Name: "nginx.service", Description: "A high performance web server",
			LoadState: "loaded", ActiveState: "active", SubState: "running"},
		UnitFileState: "enabled",
		MainPID:       1234,
		NRestarts:     2,
		Result:        "success",
		ActiveSince:   "2026-11-23T12:00:00Z",
	}
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if !fake.closed {
		t.Error("connection not closed")
	}
}

// Wrongly typed or zero properties degrade to zero values, never errors.
func TestServiceStatusToleratesOddProperties(t *testing.T) {
	got, err := systemd.ServiceStatus(context.Background(), dialer(newFake()), mustUnit(t, "backup.service"))
	if err != nil {
		t.Fatalf("ServiceStatus: %v", err)
	}
	if got.NRestarts != 0 || got.ActiveSince != "" || got.Result != "exit-code" || got.UnitFileState != "static" {
		t.Fatalf("got %+v", got)
	}
}

func TestServiceStatusNotFound(t *testing.T) {
	fake := newFake()
	got, err := systemd.ServiceStatus(context.Background(), dialer(fake), mustUnit(t, "ghost.service"))
	if err != nil {
		t.Fatalf("ServiceStatus: %v", err)
	}
	if got.LoadState != "not-found" || got.MainPID != 0 {
		t.Fatalf("got %+v", got)
	}
	// No property queries for a unit that does not exist.
	if len(fake.calls) != 1 {
		t.Fatalf("calls = %v, want only ListUnitsByNames", fake.calls)
	}
}

func TestServiceStatusRejectsZeroUnitName(t *testing.T) {
	fake := newFake()
	var zero validate.UnitName
	if _, err := systemd.ServiceStatus(context.Background(), dialer(fake), zero); err == nil {
		t.Fatal("zero UnitName accepted")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("bus was queried: %v", fake.calls)
	}
}

func TestErrorsPropagateAndClose(t *testing.T) {
	boom := errors.New("bus down")
	fake := newFake()
	fake.err = boom
	if _, err := systemd.FailedUnits(context.Background(), dialer(fake)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if !fake.closed {
		t.Error("connection not closed after error")
	}
	dialErr := func(context.Context) (systemd.Conn, error) { return nil, boom }
	if _, err := systemd.ServiceStatus(context.Background(), dialErr, mustUnit(t, "nginx.service")); !errors.Is(err, boom) {
		t.Fatalf("dial error not propagated: %v", err)
	}
}
