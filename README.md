# linux-diag-mcp

> **Status: work in progress (phase 4 of 8).** Implemented tools: `system_info`, `load_average`, `memory_usage`, `cpu_usage`.

A **read-only** [Model Context Protocol](https://modelcontextprotocol.io) server, written in Go, for diagnosing Linux systems. Security is the primary design requirement.

## Design principles

- **No free-form input**: tools accept only enums or values validated with regexes and ranges. They never accept paths, commands or arguments.
- **No external binaries**: the server reads `/proc` and `/sys` directly, and talks to systemd over D-Bus.
- **Sandboxed**: before any tool is registered, Landlock restricts the process to read-only access to `/proc`, `/sys` and `/etc/os-release`.
- **Minimal process data**: for each process it reports only pid, comm, state, cpu% and rss. It never reads `cmdline` or `environ`.
- **Bounded output**: tools return structured JSON, truncated to a configurable size, with a timeout on every tool call.
- **stdout is for the protocol only**: every log line goes to stderr.

## Tools (v1)

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

## Sandbox

At startup, before any tool is registered, the process confines itself with [Landlock](https://docs.kernel.org/userspace-api/landlock.html):

| Allowed | Denied |
|---|---|
| read files and list dirs under `/proc`, `/sys` | everything else on the filesystem (e.g. `/etc/passwd`, `/home`) |
| read `/etc/os-release` | any write, create, delete or truncate, everywhere |
| | executing files |
| | TCP bind/connect (Landlock ABI ≥ 4), UDP (ABI ≥ 10) |
| | abstract UNIX sockets and signals outside the sandbox (ABI ≥ 6) |

Landlock checks the resolved path, so magic links such as `/proc/self/root/etc/passwd` are denied too. The restriction applies to every thread and can't be lifted.

If the kernel has no Landlock support, the server logs a warning to stderr and keeps running unsandboxed. Pass `--require-sandbox` to refuse to start instead. If Landlock is present but enforcement fails, the server always refuses to start.

## Flags

| Flag | Default | Range |
|---|---|---|
| `--require-sandbox` | `false` | — |
| `--max-output-bytes` | `32768` | 1024–1048576 |
| `--tool-timeout` | `5s` | 100ms–60s |
| `--version` | — | — |

Out-of-range values and positional arguments are rejected, never clamped.

## Threat model

_TODO (phase 8)._

## Build

Requires Go ≥ 1.26. If your local Go is older, the `toolchain` line in `go.mod` makes `go` download the right toolchain automatically.

```sh
make build   # -> bin/linux-diag-mcp
make test    # go test -race ./...
make lint    # go vet + staticcheck/golangci-lint if installed
```

## Connecting

_TODO (phase 8): Claude Code / Claude Desktop, remote over SSH with a forced command, systemd unit running as a dedicated unprivileged user._
