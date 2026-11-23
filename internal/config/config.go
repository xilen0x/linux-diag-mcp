// Package config parses and validates the command-line flags.
package config

import (
	"flag"
	"fmt"
	"io"
	"time"
)

// Accepted ranges (inclusive). Values outside them are rejected rather than
// clamped, so a typo never silently changes the server's limits.
const (
	DefaultMaxOutputBytes = 32 * 1024
	MinMaxOutputBytes     = 1024
	MaxMaxOutputBytes     = 1024 * 1024

	DefaultToolTimeout = 5 * time.Second
	MinToolTimeout     = 100 * time.Millisecond
	MaxToolTimeout     = 60 * time.Second
)

// Config is the validated runtime configuration.
type Config struct {
	// RequireSandbox aborts startup when Landlock cannot be enforced.
	RequireSandbox bool
	// MaxOutputBytes caps the serialized size of every tool result.
	MaxOutputBytes int
	// ToolTimeout bounds the execution time of every tool call.
	ToolTimeout time.Duration
	// ShowVersion prints the version and exits.
	ShowVersion bool
}

// Parse parses args (without the program name). Usage and parse errors are
// written to out, which should be stderr: stdout carries JSON-RPC.
// It returns flag.ErrHelp when -h/--help is requested.
func Parse(args []string, out io.Writer) (Config, error) {
	fs := flag.NewFlagSet("linux-diag-mcp", flag.ContinueOnError)
	fs.SetOutput(out)

	var cfg Config
	fs.BoolVar(&cfg.RequireSandbox, "require-sandbox", false,
		"abort if the Landlock sandbox cannot be enforced (default: warn and continue)")
	fs.IntVar(&cfg.MaxOutputBytes, "max-output-bytes", DefaultMaxOutputBytes,
		fmt.Sprintf("maximum size of a tool result in bytes [%d-%d]", MinMaxOutputBytes, MaxMaxOutputBytes))
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", DefaultToolTimeout,
		fmt.Sprintf("maximum duration of a tool call [%s-%s]", MinToolTimeout, MaxToolTimeout))
	fs.BoolVar(&cfg.ShowVersion, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments: %d", fs.NArg())
	}
	if cfg.MaxOutputBytes < MinMaxOutputBytes || cfg.MaxOutputBytes > MaxMaxOutputBytes {
		return Config{}, fmt.Errorf("--max-output-bytes must be in [%d, %d], got %d",
			MinMaxOutputBytes, MaxMaxOutputBytes, cfg.MaxOutputBytes)
	}
	if cfg.ToolTimeout < MinToolTimeout || cfg.ToolTimeout > MaxToolTimeout {
		return Config{}, fmt.Errorf("--tool-timeout must be in [%s, %s], got %s",
			MinToolTimeout, MaxToolTimeout, cfg.ToolTimeout)
	}
	return cfg, nil
}
