package config_test

import (
	"errors"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/xilen0x/linux-diag-mcp/internal/config"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := config.Parse(nil, io.Discard)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	want := config.Config{
		RequireSandbox: false,
		MaxOutputBytes: 32 * 1024,
		ToolTimeout:    5 * time.Second,
	}
	if cfg != want {
		t.Fatalf("defaults = %+v, want %+v", cfg, want)
	}
}

func TestParseValid(t *testing.T) {
	cfg, err := config.Parse([]string{
		"--require-sandbox",
		"--max-output-bytes=65536",
		"--tool-timeout=2s",
		"--version",
	}, io.Discard)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := config.Config{
		RequireSandbox: true,
		MaxOutputBytes: 65536,
		ToolTimeout:    2 * time.Second,
		ShowVersion:    true,
	}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestParseBoundsAreInclusive(t *testing.T) {
	for _, args := range [][]string{
		{"--max-output-bytes=1024"},
		{"--max-output-bytes=1048576"},
		{"--tool-timeout=100ms"},
		{"--tool-timeout=60s"},
	} {
		if _, err := config.Parse(args, io.Discard); err != nil {
			t.Errorf("Parse(%q) = %v, want nil", args, err)
		}
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string][]string{
		"output below min":       {"--max-output-bytes=1023"},
		"output above max":       {"--max-output-bytes=1048577"},
		"output zero":            {"--max-output-bytes=0"},
		"output negative":        {"--max-output-bytes=-1"},
		"output overflow":        {"--max-output-bytes=99999999999999999999"},
		"output not a number":    {"--max-output-bytes=32k"},
		"timeout below min":      {"--tool-timeout=99ms"},
		"timeout above max":      {"--tool-timeout=61s"},
		"timeout zero":           {"--tool-timeout=0"},
		"timeout negative":       {"--tool-timeout=-5s"},
		"timeout no unit":        {"--tool-timeout=5"},
		"unknown flag":           {"--allow-write"},
		"positional argument":    {"/etc/passwd"},
		"argument after flags":   {"--require-sandbox", "extra"},
		"bool flag with garbage": {"--require-sandbox=maybe"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Parse(args, io.Discard); err == nil {
				t.Fatalf("Parse(%q) = nil error, want rejection", args)
			}
		})
	}
}

func TestParseHelp(t *testing.T) {
	_, err := config.Parse([]string{"-h"}, io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Parse(-h) = %v, want flag.ErrHelp", err)
	}
}
