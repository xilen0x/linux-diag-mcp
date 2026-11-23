// Package netdev reads per-interface traffic counters from /proc/net/dev.
package netdev

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
)

// Interfaces is the result of network_interfaces.
type Interfaces struct {
	Interfaces []Interface `json:"interfaces"`
}

// Interface holds the cumulative counters of one interface since boot.
type Interface struct {
	Name      string `json:"name"`
	RxBytes   uint64 `json:"rx_bytes"`
	RxPackets uint64 `json:"rx_packets"`
	RxErrors  uint64 `json:"rx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxBytes   uint64 `json:"tx_bytes"`
	TxPackets uint64 `json:"tx_packets"`
	TxErrors  uint64 `json:"tx_errors"`
	TxDropped uint64 `json:"tx_dropped"`
}

// Collect reads "net/dev" from procFS (rooted at /proc). Interfaces are
// those of the server's network namespace.
func Collect(procFS fs.FS) (Interfaces, error) {
	data, err := procio.ReadFile(procFS, "net/dev", procio.MaxFileBytes)
	if err != nil {
		return Interfaces{}, err
	}
	out := Interfaces{Interfaces: []Interface{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for line := 0; sc.Scan(); line++ {
		if line < 2 { // two header lines
			continue
		}
		iface, err := parseLine(sc.Text())
		if err != nil {
			return Interfaces{}, fmt.Errorf("parse /proc/net/dev line %d: %w", line+1, err)
		}
		out.Interfaces = append(out.Interfaces, iface)
	}
	return out, sc.Err()
}

// parseLine parses "name: rx(8 fields) tx(8 fields)". The name is split on
// ':' first because large counters can touch it ("eth0:123456").
func parseLine(s string) (Interface, error) {
	name, rest, ok := strings.Cut(s, ":")
	if !ok {
		return Interface{}, errors.New("missing ':'")
	}
	name = procio.CleanText(strings.TrimSpace(name))
	if name == "" {
		return Interface{}, errors.New("empty interface name")
	}
	f := strings.Fields(rest)
	if len(f) < 16 {
		return Interface{}, errors.New("too few fields")
	}
	var v [16]uint64
	for i := range v {
		n, err := strconv.ParseUint(f[i], 10, 64)
		if err != nil {
			return Interface{}, errors.New("non-numeric counter")
		}
		v[i] = n
	}
	// Receive: bytes packets errs drop fifo frame compressed multicast
	// Transmit: bytes packets errs drop fifo colls carrier compressed
	return Interface{
		Name: name, RxBytes: v[0], RxPackets: v[1], RxErrors: v[2], RxDropped: v[3],
		TxBytes: v[8], TxPackets: v[9], TxErrors: v[10], TxDropped: v[11],
	}, nil
}
