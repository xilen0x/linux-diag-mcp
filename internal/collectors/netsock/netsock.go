// Package netsock lists listening sockets from /proc/net/{tcp,udp}[6].
// Sockets are not mapped to processes.
package netsock

import (
	"bufio"
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/xilen0x/linux-diag-mcp/internal/validate"
)

// Ports is the result of listening_ports.
type Ports struct {
	Protocol  string     `json:"protocol"`
	Listeners []Listener `json:"listeners"`
}

// Listener is one local address accepting traffic.
type Listener struct {
	Family  string `json:"family" jsonschema:"ipv4 or ipv6"`
	Address string `json:"address" jsonschema:"local address; 0.0.0.0 or :: means all interfaces"`
	Port    uint16 `json:"port"`
}

// Socket states, from include/net/tcp_states.h.
const (
	stateClose  = "07" // unconnected UDP socket
	stateListen = "0A"
)

// maxLineBytes bounds one table row; real rows are ~150 bytes.
const maxLineBytes = 4096

// Collect reads the IPv4 and IPv6 tables of proto from procFS (rooted at
// /proc). The IPv4 table is required; the IPv6 one may be absent when IPv6
// is disabled. Tables are streamed: on busy hosts they hold many
// connections, of which only listeners are kept.
func Collect(procFS fs.FS, proto validate.Protocol) (Ports, error) {
	var listening func(state, remote string) bool
	switch proto {
	case validate.ProtocolTCP:
		listening = func(state, _ string) bool { return state == stateListen }
	case validate.ProtocolUDP:
		// UDP has no LISTEN state: a bound, unconnected socket receives.
		listening = func(state, remote string) bool {
			return state == stateClose && strings.HasSuffix(remote, ":0000")
		}
	default:
		return Ports{}, errors.New("netsock: invalid protocol")
	}

	seen := map[netip.AddrPort]bool{}
	for _, name := range []string{"net/" + string(proto), "net/" + string(proto) + "6"} {
		err := scan(procFS, name, listening, seen)
		if errors.Is(err, fs.ErrNotExist) && strings.HasSuffix(name, "6") {
			continue
		}
		if err != nil {
			return Ports{}, err
		}
	}

	out := Ports{Protocol: string(proto), Listeners: make([]Listener, 0, len(seen))}
	keys := make([]netip.AddrPort, 0, len(seen))
	for ap := range seen {
		keys = append(keys, ap)
	}
	slices.SortFunc(keys, func(a, b netip.AddrPort) int {
		return cmp.Or(cmp.Compare(a.Port(), b.Port()), a.Addr().Compare(b.Addr()))
	})
	for _, ap := range keys {
		family := "ipv4"
		if ap.Addr().Is6() {
			family = "ipv6"
		}
		out.Listeners = append(out.Listeners, Listener{Family: family, Address: ap.Addr().String(), Port: ap.Port()})
	}
	return out, nil
}

// scan adds the listeners of one table to seen (deduplicating sockets that
// share an address, e.g. with SO_REUSEPORT).
func scan(procFS fs.FS, name string, listening func(state, remote string) bool, seen map[netip.AddrPort]bool) error {
	f, err := procFS.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, maxLineBytes), maxLineBytes)
	for line := 1; sc.Scan(); line++ {
		if line == 1 {
			continue // header
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			return fmt.Errorf("parse /proc/%s line %d: too few fields", name, line)
		}
		if !listening(fields[3], fields[2]) {
			continue
		}
		ap, err := parseAddrPort(fields[1])
		if err != nil {
			return fmt.Errorf("parse /proc/%s line %d: %w", name, line, err)
		}
		seen[ap] = true
	}
	return sc.Err()
}

// parseAddrPort decodes "0100007F:0277". The address is printed as 32-bit
// words in host byte order, so it is decoded with the native endianness;
// the port is a plain hex number.
func parseAddrPort(s string) (netip.AddrPort, error) {
	addrHex, portHex, ok := strings.Cut(s, ":")
	if !ok || len(portHex) != 4 {
		return netip.AddrPort{}, errors.New("invalid address:port")
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, errors.New("invalid port")
	}
	if len(addrHex) != 8 && len(addrHex) != 32 {
		return netip.AddrPort{}, errors.New("invalid address length")
	}
	raw, err := hex.DecodeString(addrHex)
	if err != nil {
		return netip.AddrPort{}, errors.New("invalid address hex")
	}
	// Each 4-byte word was printed as a host-order integer: reverse that.
	for i := 0; i < len(raw); i += 4 {
		binary.NativeEndian.PutUint32(raw[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	addr, _ := netip.AddrFromSlice(raw) // length checked above
	return netip.AddrPortFrom(addr, uint16(port)), nil
}
