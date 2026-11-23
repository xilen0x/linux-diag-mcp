package sysinfo_test

import (
	"os"
	"testing"
	"testing/fstest"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/sysinfo"
)

func TestCollectUbuntuFixture(t *testing.T) {
	got, err := sysinfo.Collect(os.DirFS("testdata/ubuntu/proc"), os.DirFS("testdata/ubuntu/etc"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := sysinfo.Info{
		KernelRelease: "6.8.0-149-generic",
		UptimeSeconds: 25675.41,
		OS: sysinfo.OS{
			Name:       "Ubuntu",
			PrettyName: "Ubuntu 22.04.5 LTS",
			ID:         "ubuntu",
			VersionID:  "22.04",
			Version:    "22.04.5 LTS (Jammy Jellyfish)",
		},
	}
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func procFS(version, uptime string) fstest.MapFS {
	return fstest.MapFS{"version": {Data: []byte(version)}, "uptime": {Data: []byte(uptime)}}
}

const okVersion = "Linux version 6.1.0-test (x@y) #1 SMP\n"

func TestCollectWithoutOSRelease(t *testing.T) {
	got, err := sysinfo.Collect(procFS(okVersion, "10.5 20.0\n"), fstest.MapFS{})
	if err != nil {
		t.Fatalf("missing os-release must not fail: %v", err)
	}
	if got.OS != (sysinfo.OS{}) || got.KernelRelease != "6.1.0-test" || got.UptimeSeconds != 10.5 {
		t.Fatalf("got %+v", got)
	}
}

func TestOSReleaseQuotingAndSanitizing(t *testing.T) {
	etc := fstest.MapFS{"os-release": {Data: []byte(
		"# comment\n" +
			"NAME='Single Quoted'\n" +
			`PRETTY_NAME="Esc \"q\" \$x \\ ok"` + "\n" +
			"ID=plain\n" +
			"VERSION_ID=\"1\x1b[2J\"\n" + // control chars stripped
			"VERSION=\"unterminated\n" +
			"GARBAGE LINE\n" +
			"HOME_URL=\"https://ignored.example\"\n",
	)}}
	got, err := sysinfo.Collect(procFS(okVersion, "1 1\n"), etc)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := sysinfo.OS{
		Name:       "Single Quoted",
		PrettyName: `Esc "q" $x \ ok`,
		ID:         "plain",
		VersionID:  "1[2J",
		Version:    `"unterminated`,
	}
	if got.OS != want {
		t.Fatalf("got  %+v\nwant %+v", got.OS, want)
	}
}

func TestCollectRejectsMalformed(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"version not linux": procFS("FreeBSD 14\n", "1 1\n"),
		"version too short": procFS("Linux version\n", "1 1\n"),
		"uptime empty":      procFS(okVersion, ""),
		"uptime not number": procFS(okVersion, "abc 1\n"),
		"uptime negative":   procFS(okVersion, "-1 1\n"),
		"uptime NaN":        procFS(okVersion, "NaN 1\n"),
		"uptime Inf":        procFS(okVersion, "+Inf 1\n"),
		"version missing":   {"uptime": {Data: []byte("1 1\n")}},
	}
	for name, proc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := sysinfo.Collect(proc, fstest.MapFS{}); err == nil {
				t.Fatal("Collect succeeded, want error")
			}
		})
	}
}
