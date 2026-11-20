---
name: setup
description: Installs and starts the shared Chronicle daemon. Use when the user requests Chronicle plugin setup or the local MCP service is unavailable.
disable-model-invocation: true
---

# Set up Chronicle

Explain that this installs a pinned executable in persistent plugin data and
starts or reuses a shared per-user background daemon on loopback port 17643.
It is not an automatic hook.

## Secure-download dependency

For release installation, check `command -v gh` and `gh attestation verify --help`
before starting setup. GitHub CLI (`gh`) is the required verifier for the
GitHub-attested download method; it authenticates downloads against the trusted
repository and release workflow, rather than trusting a downloaded checksum alone.

If `gh` is missing, ask the user before downloading or installing it:

> Chronicle needs GitHub CLI (`gh`) for secure downloads: it verifies that release
> binaries were built by Chronicle's trusted GitHub Actions workflow. May I
> install it using your system's trusted package manager?

Show the proposed installation command and any elevated privileges before
requesting approval. Use the platform's trusted package manager or the official
GitHub CLI installation instructions at https://cli.github.com/. Never silently
install or upgrade `gh`, run an unverified bootstrap download, or bypass
verification. If the installed CLI lacks attestation support, ask permission to
upgrade it instead. If permission is declined, stop release setup and explain
the dependency. After installation, recheck that attestation verification is
available. Do not request or print authentication tokens in chat.

The installer verifies GitHub provenance for the pinned version against
`Emyrk/chronicle-mcp`, `.github/workflows/release.yml`, and the exact tag ref,
rejecting self-hosted runners. It rechecks installed binaries on every invocation.
If required verification flags are unsupported, ask permission to upgrade `gh`;
never remove flags to make verification pass. Network/authentication failures
also block execution. The explicitly approved `CHRONICLE_DEV_BINARY` path does
not need `gh` and is not a verified release download.

## Before a plugin upgrade

Use `/chronicle:teardown` while the old plugin is still installed, then upgrade
and run setup. The new CLI cannot stop an incompatible old daemon. If the plugin
was already upgraded, use the retained old CLI explicitly with the same state
directory for `status` and `stop`, with user approval. Active work requires
renewed explicit confirmation before `stop --force`. Do not silently shut down
or upgrade a shared service; this setup has no upgrade engine.

## Start the daemon

Ask for normal command approval before running:

```sh
sh "${CLAUDE_PLUGIN_ROOT}/scripts/chronicle.sh" start
sh "${CLAUDE_PLUGIN_ROOT}/scripts/chronicle.sh" status
```

The installer supports macOS and Linux (arm64/amd64), requires curl and GitHub CLI,
and needs an absolute private `CLAUDE_PLUGIN_DATA` directory supplied
by Claude Code. It does not require Node. The pin does not prove the release exists.
If the pinned tag has not been published, report the download failure; do not
fabricate artifacts or use another version. For local development only, the
user may explicitly select an absolute executable with `CHRONICLE_DEV_BINARY`;
this bypasses release verification and must be described as such.

Report errors, including an occupied port or incompatible running daemon. Never
kill another process or automatically stop/upgrade a daemon. After successful
status, ask the user to reconnect Chronicle in `/mcp` (or restart Claude Code),
then use `/chronicle:chronicle` to fetch live page guidance. MCP authenticates
through its headers helper; never run `auth headers` in a visible tool transcript,
print private state, or ask the user to paste tokens.
