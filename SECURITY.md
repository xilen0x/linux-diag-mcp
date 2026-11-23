# Security policy

linux-diag-mcp is built around a security boundary: read-only tools, no free-form input, and a Landlock sandbox. Reports that break that boundary are welcome and taken seriously.

## Reporting a vulnerability

Report it privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Do not open a public issue or pull request for a vulnerability.

Include:

- the version (`linux-diag-mcp --version`) and the commit if you built from source;
- the kernel version and Landlock ABI (the server logs `landlock_abi` at startup);
- the steps or the tool call that reproduces it, and what you expected instead.

You can expect an acknowledgement within 7 days. Once a fix is ready, it is released and the advisory is published with credit to you, unless you prefer to stay anonymous.

## Supported versions

Only the latest release receives security fixes.

## In scope

- Reading files or data outside what the README documents (path traversal, sandbox escape, `cmdline`/`environ` disclosure).
- Changing the system in any way: writes, starting or stopping units, executing programs.
- Making the server reflect attacker-chosen text in errors, or crash or hang beyond the documented bounds.

## Out of scope

The residual risks listed in the README's threat model are known and documented, for example what an unsandboxed run (no Landlock and no `--require-sandbox`) can do, or printable text in process names reaching the model. A report that shows one of them is worse than documented is in scope.
