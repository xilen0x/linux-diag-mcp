// Package server builds the linux-diag-mcp MCP server.
//
// It owns server identity and capabilities so that main.go (stdio) and the
// integration tests (in-memory transport) construct exactly the same server.
package server

import (
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Name is the MCP implementation name advertised to clients.
const Name = "linux-diag-mcp"

// Options configures New. The zero value is usable.
type Options struct {
	// Version advertised in serverInfo. Defaults to "dev".
	Version string
	// Logger for SDK activity. Must write to stderr: stdout carries JSON-RPC.
	// Nil disables SDK logging.
	Logger *slog.Logger
}

// New returns an MCP server with no tools registered yet.
//
// Capabilities are set explicitly: "tools" is advertised (the tool set is
// static, so listChanged is false) and the default "logging" capability is
// dropped, since this server never sends log messages to the client.
func New(opts Options) *mcp.Server {
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	return mcp.NewServer(
		&mcp.Implementation{Name: Name, Version: version},
		&mcp.ServerOptions{
			Logger:       opts.Logger,
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		},
	)
}
