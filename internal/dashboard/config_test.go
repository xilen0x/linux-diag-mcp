package dashboard

import (
	"errors"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/xilen0x/linux-diag-mcp/internal/config"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Port:           0,
		Interval:       DefaultInterval,
		MaxOutputBytes: config.DefaultMaxOutputBytes,
		ToolTimeout:    config.DefaultToolTimeout,
	}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestParseConfigAcceptsValuesInRange(t *testing.T) {
	cfg, err := ParseConfig([]string{
		"--port", "8123", "--interval", "2s", "--require-sandbox",
		"--max-output-bytes", "65536", "--tool-timeout", "10s",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Port: 8123, Interval: 2 * time.Second, RequireSandbox: true, MaxOutputBytes: 65536, ToolTimeout: 10 * time.Second}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestParseConfigBoundaries(t *testing.T) {
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{"port 0 picks a random port", []string{"--port", "0"}, true},
		{"lowest unprivileged port", []string{"--port", "1024"}, true},
		{"highest port", []string{"--port", "65535"}, true},
		{"privileged port", []string{"--port", "1023"}, false},
		{"port 1", []string{"--port", "1"}, false},
		{"port above range", []string{"--port", "65536"}, false},
		{"negative port", []string{"--port", "-1"}, false},
		{"shortest interval", []string{"--interval", "1s"}, true},
		{"longest interval", []string{"--interval", "60s"}, true},
		{"interval too short", []string{"--interval", "999ms"}, false},
		{"interval too long", []string{"--interval", "61s"}, false},
		{"output too small", []string{"--max-output-bytes", "1023"}, false},
		{"tool timeout too long", []string{"--tool-timeout", "61s"}, false},
		{"positional argument", []string{"0.0.0.0"}, false},
		{"unknown flag", []string{"--addr", "0.0.0.0:80"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig(tt.args, io.Discard)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%t", err, tt.ok)
			}
		})
	}
}

func TestParseConfigHelpAndVersion(t *testing.T) {
	if _, err := ParseConfig([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h: err = %v, want flag.ErrHelp", err)
	}
	cfg, err := ParseConfig([]string{"--version"}, io.Discard)
	if err != nil || !cfg.ShowVersion {
		t.Fatalf("--version: cfg = %+v, err = %v", cfg, err)
	}
}

func TestAllowedHosts(t *testing.T) {
	got := AllowedHosts(8123)
	want := []string{"127.0.0.1:8123", "localhost:8123"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("AllowedHosts = %v, want %v", got, want)
	}
}
