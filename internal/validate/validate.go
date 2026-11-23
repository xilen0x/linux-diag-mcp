// Package validate turns untrusted tool arguments into typed values.
//
// Every tool parameter goes through a parser here before reaching a
// collector. Parsers accept only closed enums, bounded integers or strings
// matching a strict ASCII pattern; nothing here ever becomes a path, a
// command or a shell argument. Error messages are fixed strings and never
// echo the rejected input, which may be hostile or huge.
package validate

import (
	"fmt"
	"regexp"
)

// Error reports a rejected argument without including its value.
type Error struct {
	// Field is the parameter name, e.g. "sort_by".
	Field string
	// Reason is a fixed, human-readable description of the constraint.
	Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason) }

// oneOf returns the allowed value equal to s (exact, case-sensitive match).
func oneOf[T ~string](field, s string, allowed ...T) (T, error) {
	for _, a := range allowed {
		if s == string(a) {
			return a, nil
		}
	}
	reason := "must be one of"
	for i, a := range allowed {
		if i > 0 {
			reason += ","
		}
		reason += fmt.Sprintf(" %q", string(a))
	}
	var zero T
	return zero, &Error{Field: field, Reason: reason}
}

// SortKey selects the ordering of top_processes.
type SortKey string

// Allowed SortKey values.
const (
	SortByCPU SortKey = "cpu"
	SortByMem SortKey = "mem"
)

// ParseSortKey validates the sort_by argument.
func ParseSortKey(s string) (SortKey, error) {
	return oneOf("sort_by", s, SortByCPU, SortByMem)
}

// Protocol selects the socket table read by listening_ports.
type Protocol string

// Allowed Protocol values.
const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

// ParseProtocol validates the protocol argument.
func ParseProtocol(s string) (Protocol, error) {
	return oneOf("protocol", s, ProtocolTCP, ProtocolUDP)
}

// Bounds of the top_processes limit argument.
const (
	MinLimit     = 1
	MaxLimit     = 20
	DefaultLimit = 10
)

// Limit validates the optional limit argument; nil selects DefaultLimit.
func Limit(v *int) (int, error) {
	if v == nil {
		return DefaultLimit, nil
	}
	if *v < MinLimit || *v > MaxLimit {
		return 0, &Error{Field: "limit", Reason: fmt.Sprintf("must be an integer in [%d, %d]", MinLimit, MaxLimit)}
	}
	return *v, nil
}

// UnitNamePattern is the accepted service name, from the spec. It is also
// published in the tool's input schema.
const UnitNamePattern = `^[a-zA-Z0-9@._-]{1,128}\.service$`

// MaxUnitNameLen is the longest name UnitNamePattern can match.
const MaxUnitNameLen = 128 + len(".service")

// unitNameRe is the contract from the spec. Go's RE2 engine runs in linear
// time (no ReDoS), and without the (?m) flag "$" matches only at the very
// end of the text, so a trailing "\n" cannot slip through.
var unitNameRe = regexp.MustCompile(UnitNamePattern)

// UnitName is a validated systemd service name. The zero value is invalid;
// the only way to obtain a valid one is ParseUnitName, so collectors that
// take a UnitName cannot receive an unchecked string.
type UnitName struct{ name string }

// ParseUnitName validates the unit argument against
// ^[a-zA-Z0-9@._-]{1,128}\.service$.
func ParseUnitName(s string) (UnitName, error) {
	// Cheap length gate before the regex for oversized payloads.
	if len(s) > MaxUnitNameLen || !unitNameRe.MatchString(s) {
		return UnitName{}, &Error{Field: "unit", Reason: "must match " + UnitNamePattern}
	}
	return UnitName{name: s}, nil
}

// String returns the validated name.
func (u UnitName) String() string { return u.name }

// Valid reports whether u came from ParseUnitName.
func (u UnitName) Valid() bool { return u.name != "" }
