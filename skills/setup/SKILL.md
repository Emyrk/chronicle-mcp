---
name: setup
description: Installs and starts the shared Chronicle daemon. Use when the user requests Chronicle plugin setup or the local MCP service is unavailable.
disable-model-invocation: true
---

# Set up Chronicle

Explain that this installs a pinned executable in persistent plugin data and
starts or reuses a shared per-user background daemon on loopback port 17643.
It is not an automatic hook. Ask for normal command approval before running:

```sh
sh "${CLAUDE_PLUGIN_ROOT}/scripts/chronicle.sh" start
sh "${CLAUDE_PLUGIN_ROOT}/scripts/chronicle.sh" status
```

The installer supports macOS and Linux (arm64/amd64), requires curl and sha256sum
or shasum, and needs an absolute private `CLAUDE_PLUGIN_DATA` directory supplied
by Claude Code. It does not require Node. Unreleased metadata fails clearly;
do not fabricate download URLs or checksums. For local development only, the
user may explicitly select an absolute executable with `CHRONICLE_DEV_BINARY`;
this bypasses release verification and must be described as such.

Report errors, including an occupied port or incompatible running daemon. Never
kill another process or automatically stop/upgrade a daemon. After successful
status, ask the user to reconnect Chronicle in `/mcp` (or restart Claude Code),
then use `/chronicle:chronicle` to fetch live page guidance. MCP authenticates
through its headers helper; never run `auth headers` in a visible tool transcript,
print private state, or ask the user to paste tokens.
