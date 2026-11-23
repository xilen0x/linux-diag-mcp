// Package systemd queries unit state from systemd over D-Bus.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	sd "github.com/coreos/go-systemd/v22/dbus"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
	"github.com/xilen0x/linux-diag-mcp/internal/validate"
)

// Conn is the subset of *sd.Conn used here, so tests can fake the bus.
// Only read-only methods are listed: the server cannot start, stop or
// reload anything even by mistake.
type Conn interface {
	ListUnitsFilteredContext(ctx context.Context, states []string) ([]sd.UnitStatus, error)
	ListUnitsByNamesContext(ctx context.Context, units []string) ([]sd.UnitStatus, error)
	GetUnitPropertyContext(ctx context.Context, unit, propertyName string) (*sd.Property, error)
	GetUnitTypePropertiesContext(ctx context.Context, unit, unitType string) (map[string]any, error)
	Close()
}

// Dialer opens a connection; one is opened and closed per tool call.
type Dialer func(ctx context.Context) (Conn, error)

// Unit is the common state of a systemd unit.
type Unit struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	LoadState   string `json:"load_state" jsonschema:"loaded, not-found, masked, error..."`
	ActiveState string `json:"active_state" jsonschema:"active, inactive, failed, activating..."`
	SubState    string `json:"sub_state" jsonschema:"unit-type specific state, e.g. running, exited, dead"`
}

// Failed is the result of failed_units.
type Failed struct {
	Units []Unit `json:"units"`
}

// Service is the result of service_status.
type Service struct {
	Unit
	UnitFileState string `json:"unit_file_state,omitempty" jsonschema:"enabled, disabled, static, masked..."`
	MainPID       uint32 `json:"main_pid" jsonschema:"0 when not running"`
	NRestarts     uint32 `json:"n_restarts" jsonschema:"automatic restarts since the unit was started"`
	Result        string `json:"result,omitempty" jsonschema:"outcome of the last run, e.g. success, exit-code, signal"`
	ActiveSince   string `json:"active_since,omitempty" jsonschema:"RFC 3339 time the unit last became active"`
}

// FailedUnits lists units in the failed state.
func FailedUnits(ctx context.Context, dial Dialer) (Failed, error) {
	conn, err := dial(ctx)
	if err != nil {
		return Failed{}, fmt.Errorf("connect to systemd: %w", err)
	}
	defer conn.Close()
	list, err := conn.ListUnitsFilteredContext(ctx, []string{"failed"})
	if err != nil {
		return Failed{}, fmt.Errorf("list failed units: %w", err)
	}
	out := Failed{Units: make([]Unit, 0, len(list))}
	for _, u := range list {
		out.Units = append(out.Units, toUnit(u))
	}
	// D-Bus returns units in no particular order; sort for stable output.
	slices.SortFunc(out.Units, func(a, b Unit) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ServiceStatus reports the state of one service. A name systemd does not
// know is not an error: it comes back with load_state "not-found".
func ServiceStatus(ctx context.Context, dial Dialer, unit validate.UnitName) (Service, error) {
	if !unit.Valid() {
		return Service{}, errors.New("systemd: unit name was not validated")
	}
	name := unit.String()
	conn, err := dial(ctx)
	if err != nil {
		return Service{}, fmt.Errorf("connect to systemd: %w", err)
	}
	defer conn.Close()

	list, err := conn.ListUnitsByNamesContext(ctx, []string{name})
	if err != nil {
		return Service{}, fmt.Errorf("query unit: %w", err)
	}
	if len(list) != 1 {
		return Service{}, errors.New("query unit: unexpected reply")
	}
	s := Service{Unit: toUnit(list[0])}
	if list[0].LoadState == "not-found" {
		return s, nil
	}

	// Optional details: a failure or an unexpected type leaves the zero
	// value instead of failing the whole call.
	if p, err := conn.GetUnitPropertyContext(ctx, name, "UnitFileState"); err == nil {
		if v, ok := p.Value.Value().(string); ok {
			s.UnitFileState = procio.CleanText(v)
		}
	}
	if p, err := conn.GetUnitPropertyContext(ctx, name, "ActiveEnterTimestamp"); err == nil {
		if us, ok := p.Value.Value().(uint64); ok && us > 0 && us < 1<<62 {
			s.ActiveSince = time.UnixMicro(int64(us)).UTC().Format(time.RFC3339)
		}
	}
	if props, err := conn.GetUnitTypePropertiesContext(ctx, name, "Service"); err == nil {
		s.MainPID = typed[uint32](props, "MainPID")
		s.NRestarts = typed[uint32](props, "NRestarts")
		s.Result = procio.CleanText(typed[string](props, "Result"))
	}
	return s, nil
}

// typed extracts a property of the expected type, or the zero value. It
// keeps the loosely typed D-Bus map out of the package API.
func typed[T any](props map[string]any, key string) T {
	v, _ := props[key].(T)
	return v
}

func toUnit(u sd.UnitStatus) Unit {
	return Unit{
		Name:        procio.CleanText(u.Name),
		Description: procio.CleanText(u.Description),
		LoadState:   procio.CleanText(u.LoadState),
		ActiveState: procio.CleanText(u.ActiveState),
		SubState:    procio.CleanText(u.SubState),
	}
}
