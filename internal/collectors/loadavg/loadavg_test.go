package loadavg_test

import (
	"os"
	"testing"
	"testing/fstest"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/loadavg"
)

func TestCollectFixture(t *testing.T) {
	got, err := loadavg.Collect(os.DirFS("testdata"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := loadavg.LoadAverage{Load1: 1.96, Load5: 2.15, Load15: 1.85, Runnable: 4, Total: 1382}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCollectRejectsMalformed(t *testing.T) {
	for name, data := range map[string]string{
		"empty":          "",
		"too few fields": "1.0 2.0 3.0\n",
		"bad load":       "x 2.0 3.0 1/2 9\n",
		"negative load":  "-1 2.0 3.0 1/2 9\n",
		"NaN load":       "NaN 2.0 3.0 1/2 9\n",
		"no slash":       "1 2 3 12 9\n",
		"bad runnable":   "1 2 3 a/2 9\n",
		"negative total": "1 2 3 1/-2 9\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadavg.Collect(fstest.MapFS{"loadavg": {Data: []byte(data)}})
			if err == nil {
				t.Fatal("Collect succeeded, want error")
			}
		})
	}
}
