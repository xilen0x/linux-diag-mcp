package meminfo_test

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/meminfo"
)

func TestCollectFixture(t *testing.T) {
	got, err := meminfo.Collect(os.DirFS("testdata"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// Expected values computed independently from the fixture (kB * 1024).
	want := meminfo.Memory{
		TotalBytes:      16629477376,
		FreeBytes:       1114820608,
		AvailableBytes:  9252229120,
		BuffersBytes:    494931968,
		CachedBytes:     5353172992,
		UsedBytes:       7377248256,
		UsedPercent:     44.36,
		SwapTotalBytes:  16000217088,
		SwapFreeBytes:   15768219648,
		SwapUsedBytes:   231997440,
		SwapUsedPercent: 1.45,
	}
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

const valid = `MemTotal: 1000 kB
MemFree: 100 kB
MemAvailable: 400 kB
Buffers: 10 kB
Cached: 20 kB
SwapTotal: 0 kB
SwapFree: 0 kB
`

func TestCollectNoSwap(t *testing.T) {
	got, err := meminfo.Collect(fstest.MapFS{"meminfo": {Data: []byte(valid)}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.SwapUsedPercent != 0 || got.UsedBytes != 600*1024 || got.UsedPercent != 60 {
		t.Fatalf("got %+v", got)
	}
}

func TestCollectRejectsMalformed(t *testing.T) {
	for name, data := range map[string]string{
		"empty":                "",
		"missing MemAvailable": strings.Replace(valid, "MemAvailable: 400 kB\n", "", 1),
		"not a number":         strings.Replace(valid, "MemTotal: 1000", "MemTotal: lots", 1),
		"negative":             strings.Replace(valid, "MemTotal: 1000", "MemTotal: -1000", 1),
		"overflow on kB*1024":  strings.Replace(valid, "MemTotal: 1000", "MemTotal: 18446744073709551615", 1),
		"unknown unit":         strings.Replace(valid, "MemTotal: 1000 kB", "MemTotal: 1000 MB", 1),
		"available > total":    strings.Replace(valid, "MemAvailable: 400", "MemAvailable: 4000", 1),
		"swapfree > swaptotal": strings.Replace(valid, "SwapFree: 0", "SwapFree: 5", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := meminfo.Collect(fstest.MapFS{"meminfo": {Data: []byte(data)}}); err == nil {
				t.Fatal("Collect succeeded, want error")
			}
		})
	}
}
