# linux-diag-mcp

> **Status: work in progress (phase 1 of 8).** The server starts and speaks MCP over stdio, but exposes no tools yet.

A **read-only** [Model Context Protocol](https://modelcontextprotocol.io) server, written in Go, for diagnosing Linux systems. Security is the primary design requirement.

## Design principles

- **No free-form input**: tools accept only enums or values validated with regexes and ranges. They never accept paths, commands or arguments.
- **No external binaries**: the server reads `/proc` and `/sys` directly, and talks to systemd over D-Bus.
- **Sandboxed**: before any tool is registered, Landlock restricts the process to read-only access to `/proc`, `/sys` and `/etc/os-release`.
- **Minimal process data**: for each process it reports only pid, comm, state, cpu% and rss. It never reads `cmdline` or `environ`.
- **Bounded output**: tools return structured JSON, truncated to a configurable size, with a timeout on every tool call.
- **stdout is for the protocol only**: every log line goes to stderr.

## Tools (planned, v1)

| Tool | Parameters |
|---|---|
| `system_info` | — |
| `load_average` | — |
| `memory_usage` | — |
| `cpu_usage` | — |
| `disk_usage` | — |
| `top_processes` | `sort_by`: `cpu`\|`mem`, `limit`: 1–20 |
| `network_interfaces` | — |
| `listening_ports` | `protocol`: `tcp`\|`udp` |
| `failed_units` | — |
| `service_status` | `unit`: `^[a-zA-Z0-9@._-]{1,128}\.service$` |

## Threat model

_TODO (phase 8)._

## Build

Requires Go ≥ 1.25. If your local Go is older, the `toolchain` line in `go.mod` makes `go` download the right toolchain automatically.

```sh
make build   # -> bin/linux-diag-mcp
make test    # go test -race ./...
make lint    # go vet + staticcheck/golangci-lint if installed
```

## Connecting

_TODO (phase 8): Claude Code / Claude Desktop, remote over SSH with a forced command, systemd unit running as a dedicated unprivileged user._
