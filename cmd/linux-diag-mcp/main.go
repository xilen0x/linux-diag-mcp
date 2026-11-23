// Command linux-diag-mcp is a read-only Linux diagnostics MCP server over stdio.
//
// This file only wires components together; logic lives under internal/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/disk"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/systemd"
	"github.com/xilen0x/linux-diag-mcp/internal/config"
	"github.com/xilen0x/linux-diag-mcp/internal/sandbox"
	"github.com/xilen0x/linux-diag-mcp/internal/server"
	"github.com/xilen0x/linux-diag-mcp/internal/tools"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "linux-diag-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		// stderr: stdout is reserved for the JSON-RPC protocol.
		fmt.Fprintln(os.Stderr, version)
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Load the local time zone now: /etc/localtime is unreadable once the
	// sandbox is active, and time.Local is initialized lazily.
	_, _ = time.Now().Zone()

	// The sandbox goes up before any tool is registered or any request read.
	st, err := sandbox.Apply(sandbox.Options{Require: cfg.RequireSandbox})
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	if st.Enforced {
		logger.Info("sandbox enforced", "landlock_abi", st.ABI)
	} else {
		logger.Warn("landlock unavailable: running WITHOUT sandbox (use --require-sandbox to refuse)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(server.Options{Version: version, Logger: logger})
	tools.Register(srv, tools.Deps{
		ProcFS:            os.DirFS("/proc"),
		EtcFS:             os.DirFS("/etc"),
		CPUSampleInterval: tools.DefaultCPUSampleInterval,
		Statfs:            disk.SysStatfs,
		Systemd:           systemd.DialSystemBus,
	})
	logger.Info("starting", "name", server.Name, "version", version, "transport", "stdio",
		"max_output_bytes", cfg.MaxOutputBytes, "tool_timeout", cfg.ToolTimeout)

	err = srv.Run(ctx, &mcp.StdioTransport{})
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
