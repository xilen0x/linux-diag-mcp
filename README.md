# linux-diag-mcp

A **read-only** [Model Context Protocol](https://modelcontextprotocol.io) server, written in Go, for diagnosing Linux systems. Security is the primary design requirement.

## Design principles

- **No free-form input**: tools accept only enums or values validated with regexes and ranges. They never accept paths, commands or arguments.
- **No external binaries**: the server reads `/proc` and `/sys` directly, and talks to systemd over D-Bus.
- **Sandboxed**: before any tool is registered, Landlock restricts the process to read-only access to `/proc`, `/sys` and `/etc/os-release`.
- **Minimal process data**: for each process it reports only pid, comm, state, cpu% and rss. It never reads `cmdline` or `environ`.
- **Bounded output**: tools return structured JSON, truncated to a configurable size, with a timeout on every tool call.
- **Errors never reflect input**: invalid arguments get a constant message, so a client can't make the server repeat text it chose.
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
| `connect(2)` to `/run/dbus/system_bus_socket` (ABI ≥ 9; older ABIs don't restrict UNIX sockets) | any other pathname UNIX socket (ABI ≥ 9) |
| | executing files |
| | TCP bind/connect (Landlock ABI ≥ 4), UDP (ABI ≥ 10) |
| | abstract UNIX sockets and signals outside the sandbox (ABI ≥ 6) |

The systemd tools always dial the system bus at that fixed path, ignoring `DBUS_SYSTEM_BUS_ADDRESS` (where `autolaunch:` would run `dbus-launch` and `tcp:` would open the network), and authenticate with `EXTERNAL` only. They call read-only methods: the D-Bus interface the code depends on has no method that starts, stops or reloads units. Note: the godbus dependency links `os/exec` for session-bus autolaunch, a path this server never takes; Landlock grants no execute right anywhere, so it could not run a binary anyway.

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

## Output

Every result is a JSON object matching the tool's published output schema (`structuredContent`, mirrored as JSON text in `content`). Strings that come from the system (process names, unit descriptions, mount points) are stripped of control and format characters and capped at 128 runes.

If a result exceeds `--max-output-bytes`, the longest arrays are shortened, keeping their first elements, until it fits, so the result still matches the schema. `_meta` then carries `"linux-diag-mcp/truncated": true` and `"linux-diag-mcp/original_bytes"`. A result that can't be shortened becomes the error `result too large`.

Errors are tool results with `isError: true`:

| Cause | Message |
|---|---|
| Arguments don't match the schema (unknown key, value outside the enum, range or regex) | `invalid arguments: they do not match the tool's input schema` |
| Call exceeded `--tool-timeout` | `tool timed out after 5s` |
| Panic in a tool | `internal error` (the stack trace goes to stderr) |
| Unknown tool name | JSON-RPC error `-32602`, `unknown tool or malformed request` |

The detailed reason is logged to stderr for the operator.

## Build

Requires Go ≥ 1.26. If your local Go is older, the `toolchain` line in `go.mod` makes `go` download the right toolchain automatically.

```sh
make build      # -> bin/linux-diag-mcp, bin/linux-diag-dashboard
make dashboard  # build and start the live dashboard
make test    # go test -race ./...
make lint    # go vet + staticcheck/golangci-lint if installed
```

Install:

```sh
sudo install -m 0755 bin/linux-diag-mcp bin/linux-diag-dashboard /usr/local/bin/
```

## Connecting

Always pass `--require-sandbox` in production: then a missing or blocked Landlock is a startup failure, not a warning.

### Local: Claude Code

```sh
claude mcp add linux-diag -- /usr/local/bin/linux-diag-mcp --require-sandbox
```

### Local: Claude Desktop

In `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "linux-diag": {
      "command": "/usr/local/bin/linux-diag-mcp",
      "args": ["--require-sandbox"]
    }
  }
}
```

### Remote: SSH forced command

On the server, create a dedicated account and pin its key to the server binary. A forced command runs instead of whatever the client asks for, and the options deny a terminal and every kind of forwarding:

```sh
sudo useradd --create-home --shell /bin/sh diag-mcp
```

`/home/diag-mcp/.ssh/authorized_keys` (one line):

```
command="/usr/local/bin/linux-diag-mcp --require-sandbox",no-pty,no-port-forwarding,no-agent-forwarding,no-X11-forwarding ssh-ed25519 AAAA... claude-diag
```

On the client, MCP's stdio runs over the SSH channel:

```sh
claude mcp add linux-diag-remote -- ssh -T -i ~/.ssh/claude_diag diag-mcp@server.example
```

Use a key made only for this purpose. Anyone holding it gets exactly the ten read-only tools.

### systemd: dedicated unprivileged user

[`deploy/systemd`](deploy/systemd) has a socket-activated unit: each connection to `/run/linux-diag-mcp.sock` starts one server process as the `linux-diag-mcp` system user, with no capabilities, IP sockets disabled, a seccomp filter and a read-only view of the filesystem. Only members of the `linux-diag-mcp` group can connect.

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin linux-diag-mcp
sudo install -m 0644 deploy/systemd/linux-diag-mcp.socket deploy/systemd/linux-diag-mcp@.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now linux-diag-mcp.socket
sudo usermod -aG linux-diag-mcp "$USER"   # then log in again
claude mcp add linux-diag -- socat STDIO UNIX-CONNECT:/run/linux-diag-mcp.sock
```

To serve remote clients through the hardened unit instead of running the binary as the SSH user, add `diag-mcp` to the `linux-diag-mcp` group and use `command="socat STDIO UNIX-CONNECT:/run/linux-diag-mcp.sock"` as the forced command.

`@system-service` doesn't include the Landlock syscalls on systemd ≤ 255. A seccomp filter that blocks them makes Landlock look unsupported, so without `--require-sandbox` the server would run unsandboxed. The unit allows the three syscalls explicitly.

The unit also leaves some options unset because they would break tools:

| Option | Tool it would break |
|---|---|
| `ProtectProc=invisible` | `top_processes`: other users' processes disappear |
| `ProcSubset=pid` | the tools that read `/proc/meminfo`, `/proc/stat` and `/proc/net` |
| `PrivateNetwork=yes` | `network_interfaces` and `listening_ports` describe an empty namespace |

## Live dashboard

`linux-diag-dashboard` shows every tool's output as a web page that updates itself: headline figures with short trend lines, CPU per core, memory, filesystems, top processes, listening ports, network rates and failed units.

```sh
linux-diag-dashboard --require-sandbox
```

It prints one URL to stderr, such as `http://127.0.0.1:38621/#<token>`. Open it in a browser on the same machine. The page reads the token from the URL fragment, which browsers never send to the server, keeps it for that tab and removes it from the address bar. Stopping the program invalidates the token; the next start prints a new one.

| Flag | Default | Range |
|---|---|---|
| `--port` | `0` (a free port) | 0 or 1024–65535 |
| `--interval` | `5s` | 1s–60s |
| `--require-sandbox` | `false` | — |
| `--max-output-bytes` | `32768` | 1024–1048576 |
| `--tool-timeout` | `5s` | 100ms–60s |
| `--version` | — | — |

How it stays inside the same boundary as the server:

- **Same code path.** The MCP server runs in-process over in-memory transports, so every call goes through the guard (timeout, truncation, panic recovery). Tool arguments are constants; nothing the browser sends reaches a tool. No subprocess is started.
- **Same sandbox.** The listener is bound first, then Landlock confines the whole process, HTTP handling included. New binds are denied; the socket bound before keeps accepting.
- **Loopback only.** It listens on `127.0.0.1`, which is not configurable.
- **Token for data.** `/api/snapshot` answers only `Authorization: Bearer <token>` (43 random characters, compared in constant time). The page and its assets hold no data.
- **Browser hardening.** Requests with a `Host` other than `127.0.0.1:<port>` or `localhost:<port>` are refused (DNS rebinding). Only `GET` and `HEAD` are allowed. Every response sets a CSP with no inline script, `nosniff`, `no-referrer`, `DENY` framing and `no-store`.
- **Bounded load.** One collection serves every open page for the refresh interval, and hidden tabs stop polling.

## Threat model

### What is protected

- Confidentiality of the host: file contents, secrets in other processes' `cmdline`/`environ`, other users' data.
- Integrity and availability of the host: the server must not be able to change anything.
- The model on the other side: the server must not become a channel for instructions.

### Adversaries

1. **The MCP client itself**, for example a model steered by prompt injection from content it read elsewhere. It can call any tool with any arguments, as often as it likes.
2. **Unprivileged local users** who control data the server reports: process names (`prctl(PR_SET_NAME)`), FUSE mount points, listening ports. They can try to plant instructions in tool output (indirect prompt injection).
3. **Network attackers**: there is no network listener. Remote access goes through SSH, which handles authentication.

Out of scope: an attacker who is already root, a compromised kernel, and the holder of an authorized SSH key. That key holder gets the tool surface by design.

### Threats and controls

| Threat | Control | Verified by |
|---|---|---|
| Read arbitrary files, path traversal | No tool takes a path. Landlock allows reading only `/proc`, `/sys` and `/etc/os-release`, checking the resolved path, so `/proc/self/root/...` is denied too. | `validate` tests, sandbox test re-executing the test binary under Landlock |
| Command injection | No `os/exec` in our code (depguard). systemd is reached over D-Bus. Landlock grants no execute right. | golangci-lint, sandbox test |
| Secret disclosure through processes | Only `stat`/`statm` are opened. `cmdline` and `environ` are never read. | `procs` test asserting the exact set of opened files, with decoy secrets in the fixtures |
| Changing the system | Read-only D-Bus methods behind a narrow interface. Landlock denies every write. The systemd unit uses `ProtectSystem=strict` and drops all capabilities. | sandbox test (`create`, `write /proc/self/comm` denied) |
| Unsafe D-Bus address from the environment (`autolaunch:` would exec, `tcp:` would use the network) | Fixed socket path, `EXTERNAL` authentication only. | real call with `DBUS_SYSTEM_BUS_ADDRESS=tcp:...` set |
| Reflecting client input as instructions | Constant error messages. The unknown tool name is not echoed. | integration test sending hostile payloads to every tool |
| Indirect injection through system data | Structured JSON only. Strings are stripped of control characters and capped at 128 runes. | `procio`, `systemd` tests (ANSI escapes removed) |
| Resource exhaustion | Per-call timeout that holds even when a tool ignores its context. Output cap. Bounded file reads (1 MiB). statfs runs in a worker, so a hung NFS mount can't block. systemd `TasksMax`, `MemoryMax`, `RuntimeMaxSec`. | `guard` and integration tests (timeout, panic, truncation) |
| Crash through a panic (in our code or in the SDK) | Recover in the middleware and in every goroutine we start. | `guard`, `disk` and integration tests |
| Data races | No shared mutable state between tools. | every test runs with `-race`, including concurrent calls |
| Dashboard data read from another machine or a web page | Listens on `127.0.0.1` only. Bearer token required for data, never accepted in the URL. `Host` allow-list against DNS rebinding. CSP without inline script. | `dashboard` handler tests; sandbox test proving the pre-bound listener still serves |

### Residual risks

- **The information is the product.** Process names, ports, mounts, interfaces and unit names describe the host to whoever can connect, so the access control is who can run the server: the SSH key or the socket group. With `/proc` mounted `hidepid=2`, an unprivileged server sees only its own processes. That is by design: don't run it as root.
- **Printable text still reaches the model.** A 15-character process name like `ignore_previous` passes sanitizing. The client has to treat tool output as data.
- **A call that times out keeps running in the background** until the code it is stuck in returns. The client gets its answer on time, but repeated calls stuck that way (for example statfs on a dead NFS mount) can pile up. v1 has no rate limit; under systemd, `TasksMax` and `MemoryMax` bound the damage.
- **Landlock depends on the kernel.** ABI < 4 doesn't restrict TCP, and ABI < 9 doesn't restrict connecting to UNIX sockets by path (the unit's `RestrictAddressFamilies=AF_UNIX` still blocks IP). The sandbox test runs in CI (GitHub runner, ABI 8) and on the development kernel (ABI 4), and logs the ABI it ran on. Neither enforces the ABI ≥ 9 D-Bus rule yet, so that rule is still unproven. Without Landlock and without `--require-sandbox`, the server runs unsandboxed and logs a warning.
- **The dashboard token is a bearer secret.** Anyone who sees the printed URL (terminal scrollback, a shared screen, another local user reading your terminal) can read the data until the program stops. Any local process can connect to the port, but without the token it gets only the empty page.
- **The godbus dependency links `os/exec`** for session-bus autolaunch. This server never takes that code path, and Landlock would deny the execution anyway.
