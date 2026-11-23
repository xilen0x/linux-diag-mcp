// Package server builds the linux-diag-mcp MCP server.
//
// It owns server identity and capabilities so that main.go (stdio) and the
// integration tests (in-memory transport) construct exactly the same server.
package server

import (
	"cmp"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xilen0x/linux-diag-mcp/internal/config"
	"github.com/xilen0x/linux-diag-mcp/internal/guard"
)

// Name is the MCP implementation name advertised to clients.
const Name = "linux-diag-mcp"

// Options configures New. The zero value is usable.
type Options struct {
	// Version advertised in serverInfo. Defaults to "dev".
	Version string
	// Logger for SDK and guard activity. Must write to stderr: stdout
	// carries JSON-RPC. Nil disables logging.
	Logger *slog.Logger
	// MaxOutputBytes caps every tool result; 0 selects the config default.
	MaxOutputBytes int
	// ToolTimeout bounds every tool call; 0 selects the config default.
	ToolTimeout time.Duration
}

// New returns an MCP server with no tools registered yet. Every tools/call
// goes through the guard middleware (timeout, panic recovery, error
// sanitizing, output size limit).
//
// Capabilities are set explicitly: "tools" is advertised (the tool set is
// static, so listChanged is false) and the default "logging" capability is
// dropped, since this server never sends log messages to the client.
func New(opts Options) *mcp.Server {
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	limits := guard.Limits{
		MaxOutputBytes: cmp.Or(opts.MaxOutputBytes, config.DefaultMaxOutputBytes),
		Timeout:        cmp.Or(opts.ToolTimeout, config.DefaultToolTimeout),
	}
	s := mcp.NewServer(
		&mcp.Implementation{Name: Name, Version: version},
		&mcp.ServerOptions{
			Logger:       opts.Logger,
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		},
	)
	s.AddReceivingMiddleware(guard.Middleware(limits, logger))
	return s
}
