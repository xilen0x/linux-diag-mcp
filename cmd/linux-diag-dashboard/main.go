// Command linux-diag-dashboard serves the linux-diag tools as a live web
// page on 127.0.0.1, behind a random access token.
//
// The MCP server runs in this process over in-memory transports, so every
// call goes through the same guard as over stdio. The whole process,
// HTTP handling included, runs inside the server's Landlock sandbox: the
// listening socket is bound first, because the sandbox denies new binds.
//
// This file only wires components together; logic lives under internal/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/disk"
	"github.com/xilen0x/linux-diag-mcp/internal/collectors/systemd"
	"github.com/xilen0x/linux-diag-mcp/internal/dashboard"
	"github.com/xilen0x/linux-diag-mcp/internal/sandbox"
	"github.com/xilen0x/linux-diag-mcp/internal/server"
	"github.com/xilen0x/linux-diag-mcp/internal/tools"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "linux-diag-dashboard: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := dashboard.ParseConfig(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		fmt.Fprintln(os.Stderr, version)
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Load the local time zone now: /etc/localtime is unreadable once the
	// sandbox is active, and time.Local is initialized lazily.
	_, _ = time.Now().Zone()

	token, err := dashboard.GenerateToken()
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}

	// Bind before the sandbox: Landlock (ABI >= 4) denies new TCP binds,
	// but accepting on a socket bound earlier keeps working. Classic TCP,
	// not Go's Multipath default, so the socket is the kind Landlock knows.
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	ln, err := lc.Listen(context.Background(), "tcp4", net.JoinHostPort(dashboard.ListenHost, strconv.Itoa(cfg.Port)))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

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

	srv := server.New(server.Options{
		Version:        version,
		Logger:         logger,
		MaxOutputBytes: cfg.MaxOutputBytes,
		ToolTimeout:    cfg.ToolTimeout,
	})
	tools.Register(srv, tools.Deps{
		ProcFS:            os.DirFS("/proc"),
		EtcFS:             os.DirFS("/etc"),
		CPUSampleInterval: tools.DefaultCPUSampleInterval,
		Statfs:            disk.SysStatfs,
		Systemd:           systemd.DialSystemBus,
	})
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "linux-diag-dashboard", Version: version}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		return fmt.Errorf("mcp client: %w", err)
	}
	defer func() { _ = cs.Close() }()

	handler, err := dashboard.NewHandler(dashboard.HandlerOptions{
		Token:           token,
		Snapshots:       dashboard.NewCollector(cs, cfg.Interval),
		RefreshInterval: cfg.Interval,
		AllowedHosts:    dashboard.AllowedHosts(port),
	})
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      45 * time.Second, // above the collector's 30 s bound
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// The token is printed, never logged as a field: it is the only secret.
	fmt.Fprintf(os.Stderr, "\nOpen this URL (anyone with it can read the data until you stop the program):\n\n  http://%s:%d/#%s\n\n",
		dashboard.ListenHost, port, token)
	logger.Info("serving", "addr", ln.Addr().String(), "interval", cfg.Interval, "version", version)

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
