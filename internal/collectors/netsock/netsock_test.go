package netsock_test

import (
	"os"
	"testing"
	"testing/fstest"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/netsock"
	"github.com/xilen0x/linux-diag-mcp/internal/validate"
)

func check(t *testing.T, got netsock.Ports, wantProto string, want []netsock.Listener) {
	t.Helper()
	if got.Protocol != wantProto {
		t.Errorf("protocol = %q, want %q", got.Protocol, wantProto)
	}
	if len(got.Listeners) != len(want) {
		t.Fatalf("got %d listeners:\n %+v\nwant %d:\n %+v", len(got.Listeners), got.Listeners, len(want), want)
	}
	for i := range want {
		if got.Listeners[i] != want[i] {
			t.Errorf("[%d] got %+v, want %+v", i, got.Listeners[i], want[i])
		}
	}
}

func TestTCPFixture(t *testing.T) {
	got, err := netsock.Collect(os.DirFS("testdata"), validate.ProtocolTCP)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// Decoded independently; the ESTABLISHED row and the duplicate
	// 127.0.0.1:631 listener (SO_REUSEPORT) must not appear.
	check(t, got, "tcp", []netsock.Listener{
		{Family: "ipv4", Address: "127.0.0.53", Port: 53},
		{Family: "ipv4", Address: "192.168.100.1", Port: 53},
		{Family: "ipv4", Address: "192.168.122.1", Port: 53},
		{Family: "ipv4", Address: "127.0.0.1", Port: 631},
		{Family: "ipv6", Address: "::1", Port: 631},
		{Family: "ipv4", Address: "127.0.0.1", Port: 7437},
		{Family: "ipv4", Address: "127.0.0.1", Port: 21325},
		{Family: "ipv4", Address: "127.0.0.1", Port: 32785},
		{Family: "ipv4", Address: "127.0.0.1", Port: 39021},
		{Family: "ipv4", Address: "127.0.0.1", Port: 41725},
	})
}

func TestUDPFixture(t *testing.T) {
	got, err := netsock.Collect(os.DirFS("testdata"), validate.ProtocolUDP)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// The connected socket to 203.0.113.5:53 is not a listener.
	check(t, got, "udp", []netsock.Listener{
		{Family: "ipv4", Address: "192.168.100.1", Port: 53},
		{Family: "ipv4", Address: "192.168.122.1", Port: 53},
		{Family: "ipv4", Address: "224.0.0.251", Port: 5353},
		{Family: "ipv4", Address: "0.0.0.0", Port: 41643},
		{Family: "ipv4", Address: "0.0.0.0", Port: 59414},
	})
}

const hdr = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func TestMissingIPv6TableIsTolerated(t *testing.T) {
	fsys := fstest.MapFS{"net/tcp": {Data: []byte(hdr +
		"   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1\n")}}
	got, err := netsock.Collect(fsys, validate.ProtocolTCP)
	if err != nil {
		t.Fatal(err)
	}
	check(t, got, "tcp", []netsock.Listener{{Family: "ipv4", Address: "0.0.0.0", Port: 22}})
}

func TestInvalidProtocolIsRejected(t *testing.T) {
	var zero validate.Protocol
	if _, err := netsock.Collect(os.DirFS("testdata"), zero); err == nil {
		t.Fatal("zero Protocol accepted")
	}
}

func TestCollectRejectsMalformed(t *testing.T) {
	row := func(local, rem, st string) string {
		return hdr + "   0: " + local + " " + rem + " " + st + " 00000000:00000000 00:00000000 00000000 0 0 1 1\n"
	}
	for name, data := range map[string]string{
		"missing ipv4 table": "",
		"short address":      row("0000:0016", "00000000:0000", "0A"),
		"bad hex address":    row("0000000G:0016", "00000000:0000", "0A"),
		"bad port":           row("00000000:XYZW", "00000000:0000", "0A"),
		"port too long":      row("00000000:00016", "00000000:0000", "0A"),
		"no port":            row("00000000", "00000000:0000", "0A"),
		"too few fields":     hdr + "   0: 00000000:0016\n",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			if data != "" {
				fsys["net/tcp"] = &fstest.MapFile{Data: []byte(data)}
			}
			if _, err := netsock.Collect(fsys, validate.ProtocolTCP); err == nil {
				t.Fatal("Collect succeeded, want error")
			}
		})
	}
}
