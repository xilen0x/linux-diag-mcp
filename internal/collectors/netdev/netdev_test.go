package netdev_test

import (
	"os"
	"testing"
	"testing/fstest"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/netdev"
)

func TestCollectFixture(t *testing.T) {
	got, err := netdev.Collect(os.DirFS("testdata"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []netdev.Interface{
		{Name: "lo", RxBytes: 4820538, RxPackets: 47796, TxBytes: 4820538, TxPackets: 47796},
		{Name: "enp0s31f6"},
		{Name: "wlp2s0", RxBytes: 3949398433, RxPackets: 3074067, RxDropped: 665, TxBytes: 251223969, TxPackets: 563799},
		{Name: "virbr-vmlab"},
		{Name: "virbr0"},
	}
	if len(got.Interfaces) != len(want) {
		t.Fatalf("got %d interfaces: %+v", len(got.Interfaces), got.Interfaces)
	}
	for i := range want {
		if got.Interfaces[i] != want[i] {
			t.Errorf("[%d] got %+v\n     want %+v", i, got.Interfaces[i], want[i])
		}
	}
}

const header = "Inter-|   Receive |  Transmit\n face |bytes packets|bytes packets\n"

func TestNameIsSanitized(t *testing.T) {
	data := header + "ev\x1b[31mil: 1 2 3 4 0 0 0 0 5 6 7 8 0 0 0 0\n"
	got, err := netdev.Collect(fstest.MapFS{"net/dev": {Data: []byte(data)}})
	if err != nil {
		t.Fatal(err)
	}
	want := netdev.Interface{Name: "ev[31mil", RxBytes: 1, RxPackets: 2, RxErrors: 3, RxDropped: 4,
		TxBytes: 5, TxPackets: 6, TxErrors: 7, TxDropped: 8}
	if got.Interfaces[0] != want {
		t.Fatalf("got %+v, want %+v", got.Interfaces[0], want)
	}
}

func TestCollectRejectsMalformed(t *testing.T) {
	for name, data := range map[string]string{
		"no colon":       header + "eth0 1 2 3 4 0 0 0 0 5 6 7 8 0 0 0 0\n",
		"too few fields": header + "eth0: 1 2 3\n",
		"not a number":   header + "eth0: 1 2 x 4 0 0 0 0 5 6 7 8 0 0 0 0\n",
		"negative":       header + "eth0: -1 2 3 4 0 0 0 0 5 6 7 8 0 0 0 0\n",
		"empty name":     header + ": 1 2 3 4 0 0 0 0 5 6 7 8 0 0 0 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := netdev.Collect(fstest.MapFS{"net/dev": {Data: []byte(data)}}); err == nil {
				t.Fatal("Collect succeeded, want error")
			}
		})
	}
}
