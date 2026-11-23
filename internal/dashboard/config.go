package dashboard

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/xilen0x/linux-diag-mcp/internal/config"
)

// Refresh interval bounds: how often the page polls and how long a
// snapshot is reused.
const (
	DefaultInterval = 5 * time.Second
	MinInterval     = time.Second
	MaxInterval     = 60 * time.Second
)

// Port bounds. 0 asks the kernel for a free port; privileged ports are
// refused because the dashboard never needs them.
const (
	MinPort = 1024
	MaxPort = 65535
)

// ListenHost is the only address the dashboard listens on. It is not
// configurable: the data must never be reachable from another machine.
const ListenHost = "127.0.0.1"

// Config holds the dashboard's validated flags.
type Config struct {
	Port           int
	Interval       time.Duration
	RequireSandbox bool
	MaxOutputBytes int
	ToolTimeout    time.Duration
	ShowVersion    bool
}

// ParseConfig parses the dashboard flags. Out-of-range values and
// positional arguments are rejected, never clamped.
func ParseConfig(args []string, out io.Writer) (Config, error) {
	var cfg Config
	fs := flag.NewFlagSet("linux-diag-dashboard", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.IntVar(&cfg.Port, "port", 0,
		fmt.Sprintf("port on %s; 0 picks a free one [0 or %d-%d]", ListenHost, MinPort, MaxPort))
	fs.DurationVar(&cfg.Interval, "interval", DefaultInterval,
		fmt.Sprintf("refresh interval [%s-%s]", MinInterval, MaxInterval))
	fs.BoolVar(&cfg.RequireSandbox, "require-sandbox", false, "refuse to start without Landlock")
	fs.IntVar(&cfg.MaxOutputBytes, "max-output-bytes", config.DefaultMaxOutputBytes,
		fmt.Sprintf("maximum size of a tool result in bytes [%d-%d]", config.MinMaxOutputBytes, config.MaxMaxOutputBytes))
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", config.DefaultToolTimeout,
		fmt.Sprintf("maximum duration of a tool call [%s-%s]", config.MinToolTimeout, config.MaxToolTimeout))
	fs.BoolVar(&cfg.ShowVersion, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if cfg.Port != 0 && (cfg.Port < MinPort || cfg.Port > MaxPort) {
		return Config{}, fmt.Errorf("--port must be 0 or %d-%d, got %d", MinPort, MaxPort, cfg.Port)
	}
	if cfg.Interval < MinInterval || cfg.Interval > MaxInterval {
		return Config{}, fmt.Errorf("--interval must be %s-%s, got %s", MinInterval, MaxInterval, cfg.Interval)
	}
	if cfg.MaxOutputBytes < config.MinMaxOutputBytes || cfg.MaxOutputBytes > config.MaxMaxOutputBytes {
		return Config{}, fmt.Errorf("--max-output-bytes must be %d-%d, got %d",
			config.MinMaxOutputBytes, config.MaxMaxOutputBytes, cfg.MaxOutputBytes)
	}
	if cfg.ToolTimeout < config.MinToolTimeout || cfg.ToolTimeout > config.MaxToolTimeout {
		return Config{}, fmt.Errorf("--tool-timeout must be %s-%s, got %s",
			config.MinToolTimeout, config.MaxToolTimeout, cfg.ToolTimeout)
	}
	return cfg, nil
}

// AllowedHosts returns the Host header values the handler accepts for a
// listener on port.
func AllowedHosts(port int) []string {
	p := strconv.Itoa(port)
	return []string{ListenHost + ":" + p, "localhost:" + p}
}
